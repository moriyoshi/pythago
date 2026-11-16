/*
 * VT-aware waits: locks and sockets yield the carrier to Go instead of blocking it.
 *
 * Parking protocol: a VT starts a park (vt__park_begin: bumps its park sequence and sets
 * the parked flag), publishes the returned park token to wakers (a semaphore's waiter
 * slot, an event's slot, an epoll registration, Go's deadline timer), re-checks its
 * condition, then yields VT_PARK. A waker claims the wake by clearing the flag with a CAS
 * that also checks the token's sequence (vt__unpark_tok), and the one that succeeds
 * enqueues the VT on its home carrier exactly once. A waker that arrives late - after the
 * VT moved on to another park, finished, or its struct was recycled for another VT - holds
 * a token that no longer matches and does nothing. VT structs are type-stable (never
 * freed), so the check never reads freed memory. Every wait still loops on its condition.
 *
 * Wrappers preserve errno on success: CPython reads errno after reattaching, and these
 * waits run inside that reattach (stop-the-world waits on internal locks).
 *
 * Only an unpinned VT whose thread state is detached parks. Everyone else (OS threads,
 * pinned VTs, attached waits on internal locks) blocks as before. The VT semaphore keeps
 * a futex path for those.
 *
 * build.sh wraps sem_{init,destroy,post,wait,timedwait,clockwait,trywait,getvalue} and
 * recv, send, accept, accept4, connect and poll in libpython. sem_open semaphores
 * (multiprocessing) keep glibc's layout and are passed through.
 */
#define Py_BUILD_CORE 1
#include <Python.h>
#include "internal/pycore_runtime.h"
#if PY_VERSION_HEX >= 0x030E0000
#include "internal/pycore_interp_structs.h" /* stop-the-world state (3.14 layout) */
#ifdef Py_GIL_DISABLED
#include "internal/pycore_brc.h" /* bucket table: vsem_runtime_lock_wait */
#endif
#endif

#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <linux/futex.h>
#include <poll.h>
#include <semaphore.h>
#include <stdatomic.h>
#include <sys/epoll.h>
#include <sys/eventfd.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>

#include "pythago_vt.h"

#ifdef PYTHAGO_VT

static int vt_epfd = -1, vt_evfd = -1;

/* Diagnostics: how often a VT had to block its carrier instead of parking. */
enum { ST_SEM_PARK, ST_SEM_MULTI, ST_SEM_NOPARK_VT, ST_IO_PARK, ST_IO_FAIL, ST_EV_PARK, ST_SEM_RUNTIME, ST_N };
static _Atomic int64_t vt_stats[ST_N];

void pythago_vtio_stats(int64_t *out)
{
    for (int i = 0; i < ST_N; i++) {
        out[i] = atomic_load(&vt_stats[i]);
    }
}

int pythago_vt_state(pythago_vt *vt) { return vt->state | (int)((atomic_load(&vt->park) & 1) << 8); }

pythago_vt *vt__slot(uint32_t idx);

#define PARK_SEQ_MASK 0xffffffULL

static uint64_t park_token(pythago_vt *vt, uint64_t word)
{
    return ((uint64_t)(vt->idx + 1) << 24) | ((word >> 1) & PARK_SEQ_MASK);
}

/* Only the VT itself starts or cancels its parks, and wakers only clear the flag of the
 * park their token names, so the flag is clear here and the store cannot race. */
uint64_t vt__park_begin(pythago_vt *vt)
{
    uint64_t w = atomic_load(&vt->park);
    uint64_t n = (((w >> 1) + 1) << 1) | 1;
    atomic_store(&vt->park, n);
    return park_token(vt, n);
}

int vt__unpark_self(pythago_vt *vt)
{
    uint64_t w = atomic_load(&vt->park);
    return (w & 1) && atomic_compare_exchange_strong(&vt->park, &w, w & ~1ULL);
}

pythago_vt *vt__unpark_tok(uint64_t tok)
{
    uint64_t hi = tok >> 24;
    pythago_vt *vt = hi != 0 ? vt__slot((uint32_t)(hi - 1)) : NULL;
    if (vt == NULL) {
        return NULL;
    }
    uint64_t w = atomic_load(&vt->park);
    while ((w & 1) && ((w >> 1) & PARK_SEQ_MASK) == (tok & PARK_SEQ_MASK)) {
        if (atomic_compare_exchange_weak(&vt->park, &w, w & ~1ULL)) {
            return vt;
        }
    }
    return NULL;
}

/* For Go: the token of vt's current park (read on vt's carrier after it yielded VT_PARK). */
uint64_t pythago_vt_park_token(pythago_vt *vt) { return park_token(vt, atomic_load(&vt->park)); }

int pythago_vt_home(pythago_vt *vt) { return vt->home; }

/* Nanoseconds until a parked VT's deadline (clamped at 0), or -1 without one. */
int64_t pythago_vt_timeout(pythago_vt *vt)
{
    if (vt->wake_ns < 0) {
        return -1;
    }
    int64_t d = vt->wake_ns - vt__monotonic_ns();
    return d > 0 ? d : 0;
}

int pythago_vt_io_init(void)
{
    vt_epfd = epoll_create1(EPOLL_CLOEXEC);
    vt_evfd = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
    if (vt_epfd < 0 || vt_evfd < 0) {
        return -1;
    }
    struct epoll_event ev = {.events = EPOLLIN, .data.ptr = NULL};
    return epoll_ctl(vt_epfd, EPOLL_CTL_ADD, vt_evfd, &ev);
}

static void stack_push(pythago_vt *_Atomic *head, pythago_vt *vt)
{
    pythago_vt *old = atomic_load(head);
    do {
        vt->next_ready = old;
    } while (!atomic_compare_exchange_weak(head, &old, vt));
}


/* Per-carrier local run queues. A woken, timed-out or new VT goes on its home carrier's
 * queue. A busy carrier drains it after every switch; an idle carrier sleeps on a futex
 * (its idle word) in pythago_vt_carrier_wait, and the waker wakes exactly that thread.
 * No Go channel or extra thread is involved. The idle word and the queue are each
 * written before the other is read (seq_cst), so a wakeup is never lost. */
#define MAX_CARRIERS 256
static struct {
    pythago_vt *_Atomic head;
    _Atomic int32_t idle;  /* futex word: 1 while the carrier sleeps */
    _Atomic int32_t kicked; /* Go wants the carrier's attention (e.g. pool trim) */
} local_q[MAX_CARRIERS];

static void wake_carrier(int c)
{
    if (atomic_exchange(&local_q[c].idle, 0) == 1) {
        syscall(SYS_futex, &local_q[c].idle, FUTEX_WAKE_PRIVATE, 1, NULL, NULL, 0);
    }
}

static void push_local(pythago_vt *vt)
{
    int c = vt->home;
    stack_push(&local_q[c].head, vt);
    wake_carrier(c);
}

/* For Go (timers): make vt runnable on its home carrier. */
void pythago_vt_push(pythago_vt *vt) { push_local(vt); }

/* For Go: wake carrier c even with an empty queue; pythago_vt_carrier_wait reports it. */
void pythago_vt_kick(int c)
{
    atomic_store(&local_q[c].kicked, 1);
    wake_carrier(c);
}

/* Moves up to max VTs from carrier c's queue into out (FIFO order) and returns the count. */
size_t pythago_vt_take_local(int c, uintptr_t *out, size_t max)
{
    pythago_vt *l = atomic_exchange(&local_q[c].head, NULL);
    size_t n = 0;
    pythago_vt *rest = NULL;
    while (l != NULL) { /* reverse the LIFO stack */
        pythago_vt *next = l->next_ready;
        l->next_ready = rest;
        rest = l;
        l = next;
    }
    while (rest != NULL && n < max) {
        out[n++] = (uintptr_t)rest;
        rest = rest->next_ready;
    }
    while (rest != NULL) { /* no room: give the rest back */
        pythago_vt *next = rest->next_ready;
        stack_push(&local_q[c].head, rest);
        rest = next;
    }
    return n;
}

/* Carrier c's scheduler wait: returns runnable VTs, sleeping on the idle futex while there
 * are none. Returns 0 with *kicked set when Go kicked the carrier. */
size_t pythago_vt_carrier_wait(int c, uintptr_t *out, size_t max, int32_t *kicked)
{
    for (;;) {
        *kicked = atomic_exchange(&local_q[c].kicked, 0);
        size_t n = pythago_vt_take_local(c, out, max);
        if (n > 0 || *kicked) {
            return n;
        }
        atomic_store(&local_q[c].idle, 1);
        if (atomic_load(&local_q[c].head) != NULL || atomic_load(&local_q[c].kicked)) {
            atomic_store(&local_q[c].idle, 0);
            continue;
        }
        syscall(SYS_futex, &local_q[c].idle, FUTEX_WAIT_PRIVATE, 1, NULL, NULL, 0);
        atomic_store(&local_q[c].idle, 0);
    }
}

void vt__wake_tok(uint64_t tok)
{
    pythago_vt *vt = vt__unpark_tok(tok);
    if (vt != NULL) {
        push_local(vt);
    }
}

/* For Go (deadline timers): wakes the park tok names, if it is still pending. */
void pythago_vt_wake_tok(uint64_t tok) { vt__wake_tok(tok); }

/* Hands a newly spawned VT to its home carrier. */
void vt__enqueue_new(pythago_vt *vt) { push_local(vt); }


/* Blocks until some VTs are runnable; returns them (already unparked) for Go to enqueue
 * on their home carriers. Called from Go's single I/O goroutine. */
uintptr_t pythago_vt_io_wait(size_t *n)
{
    struct epoll_event evs[256];
    for (;;) {
        int r = epoll_wait(vt_epfd, evs, 256, -1);
        if (r < 0) {
            if (errno == EINTR) {
                continue;
            }
            break;
        }
        for (int i = 0; i < r; i++) {
            uint64_t tok = evs[i].data.u64;
            if (tok != 0) {
                vt__wake_tok(tok); /* stale events (VT moved on) do nothing */
                continue;
            }
            uint64_t v;
            (void)!read(vt_evfd, &v, sizeof(v));
        }
    }
    *n = 0;
    return 0;
}

/* CPython's runtime locks that VTs treat specially (see vsem_runtime_lock_wait and
 * vt__may_park): the thread list (HEAD_LOCK), the stop-the-world mutexes, and QSBR's.
 * Their holders run short critical sections of CPython code (QSBR's spans a whole
 * stop-the-world), and several waiters cannot park: an attached thread (HEAD_LOCK does not
 * detach), a VT that has no thread state yet (creating one takes all of them), a
 * stop-the-world requester. The biased-refcounting buckets belong here too but are a
 * range (see below). */
#ifdef Py_GIL_DISABLED
static const void *runtime_lock_addr(int i)
{
    PyInterpreterState *interp = PyInterpreterState_Main();
    switch (i) {
    case 0: return &_PyRuntime.interpreters.mutex;
    case 1: return &_PyRuntime.stoptheworld.mutex;
    case 2: return &_PyRuntime.stoptheworld_mutex.bits; /* RW: global vs per-interpreter */
    case 3: return &interp->stoptheworld.mutex;
    case 4: return &interp->qsbr.mutex;
    }
    return NULL;
}
#define N_RUNTIME_LOCKS 5

/* Whether any of them is held, by anyone. HEAD_LOCK is left out: it is taken attached
 * (DONT_DETACH) and its critical sections do not wait, except a detached stop-the-world
 * requester's, which holds the stop-the-world mutexes as well. It is also taken on every
 * thread-state creation and deletion, so vetoing on it costs thread churn. */
static int runtime_lock_held(void)
{
    if (__atomic_load_n(&_PyRuntime.stoptheworld_mutex.bits, __ATOMIC_RELAXED) & ~(uintptr_t)2) {
        return 1; /* writer or readers; bit 1 is only "has parked" */
    }
    for (int i = 1; i < N_RUNTIME_LOCKS; i++) {
        if (i != 2 && (__atomic_load_n((const uint8_t *)runtime_lock_addr(i), __ATOMIC_RELAXED) & 1)) {
            return 1;
        }
    }
    return 0;
}
#else
static int runtime_lock_held(void) { return 0; }
#endif

/* A VT may park only inside CPython's detached window (Py_BEGIN_ALLOW_THREADS): it has a
 * thread state, and that state is not attached. In particular not while it has no thread
 * state yet: PyThreadState_New can stop the world (QSBR growth) and wait for the other
 * threads before the new thread state exists, and a requester that yields with the world
 * half-stopped deadlocks its carrier. */
int vt__may_park(pythago_vt *vt) { return vt__may_park_ex(vt, 0); }

/* With ignore_pin: whether vt could park were it not pinned (vt__nest_one). */
int vt__may_park_ex(pythago_vt *vt, int ignore_pin)
{
    if (vt == NULL || (vt->pinned && !ignore_pin) || PyThreadState_GetUnchecked() != NULL) {
        return 0;
    }
    /* Never while a stop-the-world is pending: its requester may itself be detached
     * (_PyEval_EnableGILTransient, when an import re-enables the GIL, detaches and then
     * stops the world), and a requester that parks never gets its carrier back because
     * every carrier blocks attaching against the half-stopped world. Blocking briefly
     * keeps whoever drives the stop-the-world on its carrier. */
    if (__atomic_load_n(&_PyRuntime.stoptheworld.requested, __ATOMIC_ACQUIRE) ||
        __atomic_load_n(&PyInterpreterState_Main()->stoptheworld.requested, __ATOMIC_ACQUIRE)) {
        return 0;
    }
    /* Never while a runtime lock is held, so that no VT parks holding one: waiters that
     * cannot park would block their carriers behind a holder that cannot run, and the
     * holder's own carrier may be one of them. We cannot tell who holds it, so this vetoes
     * every VT for the (short) critical section; they block and re-check soon
     * (vt__block_recheck_ns). Holders do reach park points: a detached stop-the-world
     * requester wakes the stopped threads through the parking lot (its contended bucket
     * locks) while still holding HEAD_LOCK and the stop-the-world mutexes. */
    if (runtime_lock_held()) {
        return 0;
    }
    /* The VT's thread state is whatever is bound in the GILState slot (the carrier swaps
     * it per VT). That covers thread states CPython creates itself (threading.Thread). */
    PyThreadState *ts = PyThread_tss_get(&_PyRuntime.autoTSSkey);
    if (ts == NULL) {
        return 0;
    }
    /* DETACHED, or SUSPENDED by another thread's stop-the-world (CPython flips detached
     * threads to suspended while the world is stopped; they are still not attached). */
    int state = __atomic_load_n(&ts->state, __ATOMIC_ACQUIRE);
    return state == 0 /* _Py_THREAD_DETACHED */ || state == 2 /* _Py_THREAD_SUSPENDED */;
}

/* A VT that must block its carrier re-checks this often whether it may park instead:
 * soon when what is in the way is transient (a runtime lock's short critical section,
 * a stop-the-world, which may leave only a short window before the next one). */
int64_t vt__block_recheck_ns(void)
{
    int transient = runtime_lock_held() ||
                    __atomic_load_n(&_PyRuntime.stoptheworld.requested, __ATOMIC_RELAXED) ||
                    __atomic_load_n(&PyInterpreterState_Main()->stoptheworld.requested, __ATOMIC_RELAXED);
    return transient ? 50000 : 1000000;
}

static int can_park(pythago_vt *vt) { return vt_evfd >= 0 && vt__may_park(vt); }

static void vt_park(pythago_vt *vt, int64_t deadline_ns)
{
    vt->wake_ns = deadline_ns;
    vt__yield(vt, VT_PARK);
    vt->wake_ns = -1;
}

/* ---- semaphores ---- */

#define VSEM_MAGIC 0x5370676168747970ULL /* "pythagpS" */

/* A VT-aware semaphore inside sem_t: one 64-bit word = count (bits 0-15) | waiter
 * (bits 16-63: 0 none, 1 an OS thread on the futex, else the parked VT's park token).
 *
 * CPython's parking lot keeps the semaphore on the waiter's stack and, per POSIX, the
 * waiter may destroy it as soon as a post is visible. So a post is a single CAS that
 * increments the count and takes the waiter at once, and touches the semaphore no more:
 * afterwards it only wakes the VT (which outlives the wait) or issues a futex wake
 * (harmless on freed memory). */
typedef struct {
    uint64_t magic;
    _Atomic uint64_t word;
} vsem;

_Static_assert(sizeof(vsem) <= sizeof(sem_t), "vsem must fit in sem_t");

#define VSEM_COUNT(w) ((w) & 0xffff)
#define VSEM_WAITER(w) ((w) >> 16)
#define VSEM_OS_WAITER 1

static vsem *vsem_of(sem_t *s)
{
    vsem *v = (vsem *)s;
    return v->magic == VSEM_MAGIC ? v : NULL;
}

/* The futex word is the low half (count plus low waiter bits). */
static uint32_t *vsem_futex(vsem *v) { return (uint32_t *)&v->word; }

static int vsem_trydec(vsem *v)
{
    uint64_t w = atomic_load(&v->word);
    while (VSEM_COUNT(w) > 0) {
        if (atomic_compare_exchange_weak(&v->word, &w, w - 1)) {
            return 1;
        }
    }
    return 0;
}

/* Sets the waiter field from 0 to who while the count is 0; returns 0 if the count is
 * positive or another waiter is registered. */
static int vsem_register(vsem *v, uint64_t who)
{
    uint64_t w = atomic_load(&v->word);
    while (VSEM_COUNT(w) == 0 && VSEM_WAITER(w) == 0) {
        if (atomic_compare_exchange_weak(&v->word, &w, w | (who << 16))) {
            return 1;
        }
    }
    return 0;
}

static void vsem_unregister(vsem *v, uint64_t who)
{
    uint64_t w = atomic_load(&v->word);
    while (VSEM_WAITER(w) == who) {
        if (atomic_compare_exchange_weak(&v->word, &w, VSEM_COUNT(w))) {
            return;
        }
    }
}

/* Whether v is a parking-lot wait for a runtime lock (above) or a biased-refcounting
 * bucket mutex. Python's parking lot keeps the address waited on right before the
 * semaphore (struct wait_entry {park_arg; addr; sema; ...} in Python/parking_lot.c);
 * raw mutex waits ({next; sema}) hold NULL or another waiter's stack entry there, and
 * PyThread_allocate_lock's semaphores (heap blocks of their own) the allocator's header
 * or a neighbouring block; never one of these addresses.
 *
 * A VT never parks in such a wait. PyMutex wakes the oldest waiter first and, once it
 * has waited over a millisecond, hands it the mutex even if it is a parked VT. A VT
 * parked in the queue would thus take the wake, or the mutex itself, while a waiter that
 * cannot park blocks the VT's carrier: the carrier switching refcount owners
 * (carrier_brc_owner), a VT creating or deleting its thread state, an attached HEAD_LOCK
 * waiter, a stop-the-world requester. Never parked, a waiter for these locks is always
 * running or about to, and since no VT parks holding one (vt__may_park), every wait is
 * bounded by a short critical section.
 *
 * Raw mutexes (the parking lot's own bucket locks) need no rule: they wake the newest
 * waiter first, and a carrier-blocking waiter is always newer than any VT of its carrier
 * parked on the same lock. */
static int vsem_runtime_lock_wait(const vsem *v)
{
#ifdef Py_GIL_DISABLED
    uintptr_t addr = ((const uintptr_t *)v)[-1];
    const struct _brc_state *brc = &PyInterpreterState_Main()->brc;
    if (addr - (uintptr_t)brc->table < sizeof(brc->table)) {
        return 1;
    }
    for (int i = 0; i < N_RUNTIME_LOCKS; i++) {
        if (addr == (uintptr_t)runtime_lock_addr(i)) {
            return 1;
        }
    }
#else
    (void)v;
#endif
    return 0;
}

static int vsem_wait(vsem *v, int64_t deadline)
{
    int saved_errno = errno; /* the futex wait below sets it even when we succeed */
    for (;;) {
        if (vsem_trydec(v)) {
            errno = saved_errno;
            return 0;
        }
        int64_t now = vt__monotonic_ns();
        if (deadline >= 0 && now >= deadline) {
            errno = ETIMEDOUT;
            return -1;
        }
        pythago_vt *vt = vt__current();
        if (can_park(vt) && !vsem_runtime_lock_wait(v)) {
            uint64_t tok = vt__park_begin(vt);
            if (vsem_register(v, tok)) {
                atomic_fetch_add(&vt_stats[ST_SEM_PARK], 1);
                vt_park(vt, deadline);
                vsem_unregister(v, tok); /* still ours after a timeout */
                continue;
            }
            /* Count became positive, or another waiter holds the slot. */
            if (!vt__unpark_self(vt)) {
                vt_park(vt, -1); /* cannot happen without registration; be safe */
            }
            if (VSEM_COUNT(atomic_load(&v->word)) > 0) {
                continue;
            }
            atomic_fetch_add(&vt_stats[ST_SEM_MULTI], 1);
        } else if (vt != NULL) {
            if (!vsem_runtime_lock_wait(v) && vt__nest_one(vt)) {
                continue; /* pinned: ran a VT of this carrier instead of blocking */
            }
            atomic_fetch_add(&vt_stats[vsem_runtime_lock_wait(v) ? ST_SEM_RUNTIME : ST_SEM_NOPARK_VT], 1);
        }
        /* Block this OS thread on the futex (a VT re-checks every millisecond whether
         * it may park instead; a second waiter just polls). */
        int registered = vsem_register(v, VSEM_OS_WAITER);
        uint64_t w = atomic_load(&v->word);
        if (VSEM_COUNT(w) == 0) {
            struct timespec rel, *rp = NULL;
            int64_t d = deadline >= 0 ? deadline - now : INT64_MAX;
            if (vt != NULL || !registered) {
                int64_t rc = vt != NULL ? vt__block_slice(vt) : vt__block_recheck_ns();
                d = d > rc ? rc : d;
            }
            if (d != INT64_MAX) {
                d = d > 0 ? d : 0;
                rel.tv_sec = d / 1000000000;
                rel.tv_nsec = d % 1000000000;
                rp = &rel;
            }
            syscall(SYS_futex, vsem_futex(v), FUTEX_WAIT_PRIVATE, (uint32_t)w, rp, NULL, 0);
        }
        if (registered) {
            vsem_unregister(v, VSEM_OS_WAITER);
        }
    }
}

static int64_t abs_to_monotonic(clockid_t clock, const struct timespec *abs)
{
    int64_t t = (int64_t)abs->tv_sec * 1000000000 + abs->tv_nsec;
    if (clock == CLOCK_MONOTONIC) {
        return t;
    }
    struct timespec now;
    clock_gettime(clock, &now);
    return vt__monotonic_ns() + (t - ((int64_t)now.tv_sec * 1000000000 + now.tv_nsec));
}

int __wrap_sem_init(sem_t *s, int pshared, unsigned value)
{
    if (pshared || value > 0xffff) {
        return sem_init(s, pshared, value);
    }
    vsem *v = (vsem *)s;
    atomic_store(&v->word, (uint64_t)value);
    v->magic = VSEM_MAGIC;
    return 0;
}

int __wrap_sem_destroy(sem_t *s)
{
    vsem *v = vsem_of(s);
    if (v == NULL) {
        return sem_destroy(s);
    }
    v->magic = 0;
    return 0;
}

int __wrap_sem_post(sem_t *s)
{
    vsem *v = vsem_of(s);
    if (v == NULL) {
        return sem_post(s);
    }
    uint64_t w = atomic_load(&v->word), n;
    do {
        if (VSEM_COUNT(w) == 0xffff) {
            errno = EOVERFLOW;
            return -1;
        }
        n = VSEM_COUNT(w) + 1; /* take the waiter along with the increment */
    } while (!atomic_compare_exchange_weak(&v->word, &w, n));
    /* v may be gone from here on. The waiter is a token, so a VT that already left (or
     * whose struct now runs another thread) is not woken. */
    uint64_t waiter = VSEM_WAITER(w);
    if (waiter == VSEM_OS_WAITER) {
        int saved_errno = errno;
        syscall(SYS_futex, vsem_futex(v), FUTEX_WAKE_PRIVATE, INT_MAX, NULL, NULL, 0);
        errno = saved_errno;
    } else if (waiter != 0) {
        vt__wake_tok(waiter);
    }
    return 0;
}

int __wrap_sem_wait(sem_t *s)
{
    vsem *v = vsem_of(s);
    return v != NULL ? vsem_wait(v, -1) : sem_wait(s);
}

int __wrap_sem_timedwait(sem_t *s, const struct timespec *abs)
{
    vsem *v = vsem_of(s);
    return v != NULL ? vsem_wait(v, abs_to_monotonic(CLOCK_REALTIME, abs)) : sem_timedwait(s, abs);
}

int __wrap_sem_clockwait(sem_t *s, clockid_t clock, const struct timespec *abs)
{
    vsem *v = vsem_of(s);
    return v != NULL ? vsem_wait(v, abs_to_monotonic(clock, abs)) : sem_clockwait(s, clock, abs);
}

int __wrap_sem_trywait(sem_t *s)
{
    vsem *v = vsem_of(s);
    if (v == NULL) {
        return sem_trywait(s);
    }
    if (vsem_trydec(v)) {
        return 0;
    }
    errno = EAGAIN;
    return -1;
}

int __wrap_sem_getvalue(sem_t *s, int *value)
{
    vsem *v = vsem_of(s);
    if (v == NULL) {
        return sem_getvalue(s, value);
    }
    *value = (int)VSEM_COUNT(atomic_load(&v->word));
    return 0;
}

/* ---- sockets ---- */

static int can_io(pythago_vt *vt) { return can_park(vt) && vt_epfd >= 0; }

static int fd_nonblocking(int fd) { return (fcntl(fd, F_GETFL) & O_NONBLOCK) != 0; }

/* Parks vt until one of fds is ready or the deadline passes. Each fd is registered
 * through a dup, so several VTs can wait on the same socket. Returns -1 (nothing waited)
 * if registration failed. */
static int vt_wait_fds(pythago_vt *vt, const int *fds, const uint32_t *events, int n,
                       int64_t deadline)
{
    int dups[64];
    int k = 0;
    uint64_t tok = vt__park_begin(vt);
    for (; k < n; k++) {
        int d = fcntl(fds[k], F_DUPFD_CLOEXEC, 0);
        if (d < 0) {
            break;
        }
        struct epoll_event ev = {.events = events[k] | EPOLLONESHOT | EPOLLERR | EPOLLHUP,
                                 .data.u64 = tok};
        if (epoll_ctl(vt_epfd, EPOLL_CTL_ADD, d, &ev) != 0) {
            close(d);
            break;
        }
        dups[k] = d;
    }
    int ok = k == n;
    atomic_fetch_add(&vt_stats[ok ? ST_IO_PARK : ST_IO_FAIL], 1);
    if (ok) {
        vt_park(vt, deadline);
    } else if (!vt__unpark_self(vt)) {
        vt_park(vt, -1); /* an event raced in before we gave up; consume it */
    }
    for (int i = 0; i < k; i++) {
        epoll_ctl(vt_epfd, EPOLL_CTL_DEL, dups[i], NULL);
        close(dups[i]);
    }
    return ok ? 0 : -1;
}

/* Waits until one of fds may be ready (spurious returns are fine: callers retry) or the
 * deadline passes. A VT parks. One that may not park right now (a stop-the-world is
 * pending, a runtime lock is held; or for good: pinned, attached) waits in short slices
 * and re-checks: blocking in the syscall itself could hold its carrier for the whole
 * call, and the peer may be a VT on that very carrier. Other threads just block.
 * EPOLLIN/OUT/PRI have the values of POLLIN/OUT/PRI. */
static void io_wait(pythago_vt *vt, const int *fds, const uint32_t *events, int n,
                    int64_t deadline)
{
    if (can_io(vt) && vt_wait_fds(vt, fds, events, n, deadline) == 0) {
        return;
    }
    if (vt__nest_one(vt)) {
        return; /* pinned: ran a VT of this carrier instead; the caller retries */
    }
    struct pollfd p[64];
    for (int i = 0; i < n; i++) {
        p[i] = (struct pollfd){.fd = fds[i], .events = (short)events[i]};
    }
    int64_t d = vt != NULL ? vt__block_slice(vt) : -1;
    if (deadline >= 0) {
        int64_t left = deadline - vt__monotonic_ns();
        left = left > 0 ? left : 0;
        d = d < 0 || left < d ? left : d;
    }
    struct timespec ts = {.tv_sec = d / 1000000000, .tv_nsec = d % 1000000000};
    int saved = errno;
    ppoll(p, n, d < 0 ? NULL : &ts, NULL);
    errno = saved;
}

static void wait_one(pythago_vt *vt, int fd, uint32_t ev) { io_wait(vt, &fd, &ev, 1, -1); }

ssize_t __wrap_recv(int fd, void *buf, size_t len, int flags)
{
    pythago_vt *vt = vt__current();
    if (vt == NULL || (flags & MSG_DONTWAIT)) {
        return recv(fd, buf, len, flags);
    }
    for (;;) {
        ssize_t r = recv(fd, buf, len, flags | MSG_DONTWAIT);
        if (r >= 0 || (errno != EAGAIN && errno != EWOULDBLOCK) || fd_nonblocking(fd)) {
            return r;
        }
        wait_one(vt, fd, EPOLLIN);
    }
}

ssize_t __wrap_send(int fd, const void *buf, size_t len, int flags)
{
    pythago_vt *vt = vt__current();
    if (vt == NULL || (flags & MSG_DONTWAIT)) {
        return send(fd, buf, len, flags);
    }
    for (;;) {
        ssize_t r = send(fd, buf, len, flags | MSG_DONTWAIT);
        if (r >= 0 || (errno != EAGAIN && errno != EWOULDBLOCK) || fd_nonblocking(fd)) {
            return r;
        }
        wait_one(vt, fd, EPOLLOUT);
    }
}

/* accept has no per-call non-blocking flag, so a blocking listener a VT accepts on is
 * switched to O_NONBLOCK for good, and every wrapped accept on it emulates blocking.
 * Listeners are identified by inode so a reused fd number is not mistaken for one. */
#define NB_MAX 65536
static _Atomic uint64_t nb_ino[NB_MAX];

static int nb_switched(int fd)
{
    struct stat st;
    return fd >= 0 && fd < NB_MAX && atomic_load(&nb_ino[fd]) != 0 && fstat(fd, &st) == 0 &&
           atomic_load(&nb_ino[fd]) == (uint64_t)st.st_ino;
}

static int do_accept(int fd, struct sockaddr *addr, socklen_t *len, int flags, int use4)
{
    return use4 ? accept4(fd, addr, len, flags) : accept(fd, addr, len);
}

static int vt_accept(int fd, struct sockaddr *addr, socklen_t *len, int flags, int use4)
{
    pythago_vt *vt = vt__current();
    int ours = nb_switched(fd);
    if (!ours && vt != NULL && fd < NB_MAX && !fd_nonblocking(fd)) {
        struct stat st;
        if (fstat(fd, &st) == 0 && fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK) == 0) {
            atomic_store(&nb_ino[fd], (uint64_t)st.st_ino);
            ours = 1;
        }
    }
    for (;;) {
        int r = do_accept(fd, addr, len, flags, use4);
        if (r >= 0 || (errno != EAGAIN && errno != EWOULDBLOCK) || !ours) {
            return r;
        }
        wait_one(vt__current(), fd, EPOLLIN);
    }
}

int __wrap_accept(int fd, struct sockaddr *addr, socklen_t *len)
{
    return vt_accept(fd, addr, len, 0, 0);
}

int __wrap_accept4(int fd, struct sockaddr *addr, socklen_t *len, int flags)
{
    return vt_accept(fd, addr, len, flags, 1);
}

int __wrap_connect(int fd, const struct sockaddr *addr, socklen_t len)
{
    pythago_vt *vt = vt__current();
    if (vt == NULL || fd_nonblocking(fd)) {
        return connect(fd, addr, len);
    }
    int fl = fcntl(fd, F_GETFL);
    fcntl(fd, F_SETFL, fl | O_NONBLOCK);
    int r = connect(fd, addr, len);
    if (r != 0 && errno == EINPROGRESS) {
        /* Wakes can be spurious and poll can be interrupted: wait until the socket
         * really is writable (connected or failed) before reading SO_ERROR. */
        for (;;) {
            struct pollfd p = {.fd = fd, .events = POLLOUT};
            if (poll(&p, 1, 0) > 0) {
                break;
            }
            wait_one(vt, fd, EPOLLOUT);
        }
        int err = 0;
        socklen_t el = sizeof(err);
        if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &el) != 0) {
            err = errno;
        }
        r = err ? -1 : 0;
        errno = err;
    }
    int saved = errno;
    fcntl(fd, F_SETFL, fl);
    errno = saved;
    return r;
}

int __wrap_poll(struct pollfd *fds, nfds_t n, int timeout)
{
    pythago_vt *vt = vt__current();
    if (vt == NULL || timeout == 0 || n == 0 || n > 64) {
        return poll(fds, n, timeout);
    }
    int64_t deadline = timeout < 0 ? -1 : vt__monotonic_ns() + (int64_t)timeout * 1000000;
    for (;;) {
        int r = poll(fds, n, 0);
        if (r != 0) {
            return r;
        }
        if (deadline >= 0 && vt__monotonic_ns() >= deadline) {
            return 0;
        }
        int fdv[64];
        uint32_t ev[64];
        int m = 0;
        for (nfds_t i = 0; i < n; i++) {
            if (fds[i].fd < 0) {
                continue;
            }
            fdv[m] = fds[i].fd;
            ev[m++] = ((fds[i].events & POLLIN) ? EPOLLIN : 0) |
                      ((fds[i].events & POLLOUT) ? EPOLLOUT : 0) |
                      ((fds[i].events & POLLPRI) ? EPOLLPRI : 0);
        }
        if (m == 0) { /* nothing to watch: a plain timeout */
            int ms = deadline < 0 ? -1 : (int)((deadline - vt__monotonic_ns()) / 1000000);
            return poll(fds, n, ms < 0 && deadline >= 0 ? 0 : ms);
        }
        io_wait(vt, fdv, ev, m, deadline);
    }
}

#else /* !PYTHAGO_VT: pass-through wrappers */

int __wrap_sem_init(sem_t *s, int pshared, unsigned value) { return sem_init(s, pshared, value); }
int __wrap_sem_destroy(sem_t *s) { return sem_destroy(s); }
int __wrap_sem_post(sem_t *s) { return sem_post(s); }
int __wrap_sem_wait(sem_t *s) { return sem_wait(s); }
int __wrap_sem_timedwait(sem_t *s, const struct timespec *abs) { return sem_timedwait(s, abs); }
int __wrap_sem_clockwait(sem_t *s, clockid_t c, const struct timespec *abs)
{
    return sem_clockwait(s, c, abs);
}
int __wrap_sem_trywait(sem_t *s) { return sem_trywait(s); }
int __wrap_sem_getvalue(sem_t *s, int *value) { return sem_getvalue(s, value); }
ssize_t __wrap_recv(int fd, void *buf, size_t len, int flags) { return recv(fd, buf, len, flags); }
ssize_t __wrap_send(int fd, const void *buf, size_t len, int flags)
{
    return send(fd, buf, len, flags);
}
int __wrap_accept(int fd, struct sockaddr *a, socklen_t *l) { return accept(fd, a, l); }
int __wrap_accept4(int fd, struct sockaddr *a, socklen_t *l, int f) { return accept4(fd, a, l, f); }
int __wrap_connect(int fd, const struct sockaddr *a, socklen_t l) { return connect(fd, a, l); }
int __wrap_poll(struct pollfd *fds, nfds_t n, int timeout) { return poll(fds, n, timeout); }

#endif

/* ---- events: how an asyncio loop sleeps until Go has completions ----
 *
 * Goroutines bump seq (pythago_vtev_set) when they queue work for the loop. The loop
 * thread reads seq, drains Go's queue, and if it was empty waits for seq to move
 * (ev_wait). On a VT the wait parks the VT, so its carrier keeps running other VTs;
 * elsewhere it is a futex wait. Both builtins are plain C (METH_FASTCALL), so neither
 * runs Go code on the loop thread. */
#include <limits.h>

struct pythago_vtev {
    _Atomic uint32_t seq;
    _Atomic int32_t os_waiters;
#ifdef PYTHAGO_VT
    _Atomic uint64_t vt; /* park token of the VT waiting here, or 0 */
#endif
};

pythago_vtev *pythago_vtev_new(void) { return calloc(1, sizeof(pythago_vtev)); }

void pythago_vtev_set(pythago_vtev *e)
{
    atomic_fetch_add(&e->seq, 1);
#ifdef PYTHAGO_VT
    uint64_t w = atomic_exchange(&e->vt, 0);
    if (w != 0) {
        vt__wake_tok(w);
    }
#endif
    if (atomic_load(&e->os_waiters) > 0) {
        syscall(SYS_futex, &e->seq, FUTEX_WAKE_PRIVATE, INT_MAX, NULL, NULL, 0);
    }
}

uint32_t vt__ev_seq(pythago_vtev *e) { return atomic_load(&e->seq); }

static void ev_wait(pythago_vtev *e, uint32_t s, int64_t deadline)
{
    for (;;) {
        if (atomic_load(&e->seq) != s) {
            return;
        }
        int64_t now = vt__monotonic_ns();
        if (deadline >= 0 && now >= deadline) {
            return;
        }
#ifdef PYTHAGO_VT
        pythago_vt *vt = vt__current();
        if (can_park(vt)) {
            uint64_t none = 0, tok = vt__park_begin(vt);
            if (atomic_compare_exchange_strong(&e->vt, &none, tok)) {
                uint64_t me = tok;
                if (atomic_load(&e->seq) != s) {
                    atomic_compare_exchange_strong(&e->vt, &me, 0);
                    if (!vt__unpark_self(vt)) {
                        vt_park(vt, -1); /* a set already queued us; consume it */
                    }
                    return;
                }
                atomic_fetch_add(&vt_stats[ST_EV_PARK], 1);
                vt_park(vt, deadline);
                me = tok;
                atomic_compare_exchange_strong(&e->vt, &me, 0);
                continue;
            }
            /* Another VT waits here; fall back to the futex. Our park was published to
             * nobody, but cancel it the same way as everywhere else. */
            if (!vt__unpark_self(vt)) {
                vt_park(vt, -1);
            }
        }
        if (vt__nest_one(vt)) {
            continue; /* pinned: ran a VT of this carrier instead of blocking */
        }
#endif
        atomic_fetch_add(&e->os_waiters, 1);
        if (atomic_load(&e->seq) == s) {
            struct timespec rel, *rp = NULL;
            int64_t d = deadline >= 0 ? deadline - now : INT64_MAX;
#ifdef PYTHAGO_VT
            if (vt__current() != NULL) { /* a VT that could not park re-checks soon */
                int64_t rc = vt__block_slice(vt__current());
                d = d > rc ? rc : d;
            }
#endif
            if (d != INT64_MAX) {
                rel.tv_sec = d / 1000000000;
                rel.tv_nsec = d % 1000000000;
                rp = &rel;
            }
            syscall(SYS_futex, &e->seq, FUTEX_WAIT_PRIVATE, s, rp, NULL, 0);
        }
        atomic_fetch_sub(&e->os_waiters, 1);
    }
}

void vt__ev_wait(pythago_vtev *e, uint32_t s, int64_t deadline)
{
    int saved_errno = errno;
    ev_wait(e, s, deadline);
    errno = saved_errno;
}

/* ev_seq(ev) -> int */
PyObject *pythago_vtev_seq_py(PyObject *self, PyObject *const *args, Py_ssize_t nargs)
{
    if (nargs != 1) {
        PyErr_SetString(PyExc_TypeError, "ev_seq(ev)");
        return NULL;
    }
    pythago_vtev *e = PyLong_AsVoidPtr(args[0]);
    return e == NULL ? NULL : PyLong_FromUnsignedLong(atomic_load(&e->seq));
}

/* ev_wait(ev, seq, timeout_seconds_or_None): returns once seq moved or on timeout. */
PyObject *pythago_vtev_wait_py(PyObject *self, PyObject *const *args, Py_ssize_t nargs)
{
    if (nargs != 3) {
        PyErr_SetString(PyExc_TypeError, "ev_wait(ev, seq, timeout)");
        return NULL;
    }
    pythago_vtev *e = PyLong_AsVoidPtr(args[0]);
    uint32_t s = (uint32_t)PyLong_AsUnsignedLong(args[1]);
    int64_t deadline = -1;
    if (args[2] != Py_None) {
        double t = PyFloat_AsDouble(args[2]);
        deadline = vt__monotonic_ns() + (t > 0 ? (int64_t)(t * 1e9) : 0);
    }
    if (e == NULL || PyErr_Occurred()) {
        return NULL;
    }
    Py_BEGIN_ALLOW_THREADS
    vt__ev_wait(e, s, deadline);
    Py_END_ALLOW_THREADS
    Py_RETURN_NONE;
}
