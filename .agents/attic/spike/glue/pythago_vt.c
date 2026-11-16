#define _GNU_SOURCE 1
/*
 * pythago virtual threads (VTs): Python threads multiplexed M:N over carrier OS threads.
 *
 * A VT has its own C stack and a PyThreadState. It runs on one home carrier for its whole
 * life. Free-threaded CPython keys object ownership (ob_tid), mimalloc's local free path
 * and biased refcounting on the OS thread pointer, so VTs that share a carrier (and never
 * run concurrently) keep those invariants. Migrating a VT between carriers would break them.
 *
 * VTs switch only inside wrapped blocking libc calls, after CPython has detached the
 * thread state (Py_BEGIN_ALLOW_THREADS). The carrier then runs other VTs, and Go decides
 * when the blocked VT is runnable again.
 *
 * build.sh links libpython with --wrap for clock_nanosleep, pthread_self and
 * pthread_getattr_np. pthread_self returns a per-VT id, so get_ident(), RLock owners and
 * the import lock follow the VT rather than the carrier. Locks and sockets are in
 * pythago_vtio.c.
 */
#define Py_BUILD_CORE 1
#include <Python.h>
#include "internal/pycore_runtime.h"

#include <errno.h>
#include <pthread.h>
#include <stdint.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>

#include "pythago_vt.h"

static __thread pythago_vt *current_vt;

/* noinline so the TLS address is computed fresh after every context switch. */
static __attribute__((noinline)) pythago_vt *vt_current(void) { return current_vt; }
pythago_vt *vt__current(void) { return vt_current(); }
static __attribute__((noinline)) void vt_set_current(pythago_vt *vt) { current_vt = vt; }

static int64_t monotonic_ns(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000 + ts.tv_nsec;
}
int64_t vt__monotonic_ns(void) { return monotonic_ns(); }

/* ---- wrappers (always present; libpython is linked against them) ---- */

pthread_t __wrap_pthread_self(void)
{
    pythago_vt *vt = vt_current();
    return vt != NULL ? vt->ident : pthread_self();
}

int __wrap_pthread_getattr_np(pthread_t th, pthread_attr_t *attr)
{
    pythago_vt *vt = vt_current();
    if (vt != NULL && th == vt->ident) {
        /* CPython derives its C stack limits from this; report the VT stack. */
        int rc = pthread_attr_init(attr);
        return rc != 0 ? rc : pthread_attr_setstack(attr, vt->stack, vt->stack_size);
    }
    return pthread_getattr_np(th, attr);
}

#ifdef PYTHAGO_VT
/* void pythago_ctx_switch(void **save_sp, void *load_sp): saves callee-saved state on
 * the current stack, stores sp in *save_sp, then restores the context at load_sp. */
__asm__(
    ".text\n"
    ".p2align 2\n"
    ".type pythago_ctx_switch, %function\n"
    "pythago_ctx_switch:\n"
    "  sub sp, sp, #160\n"
    "  stp x19, x20, [sp, #0]\n"
    "  stp x21, x22, [sp, #16]\n"
    "  stp x23, x24, [sp, #32]\n"
    "  stp x25, x26, [sp, #48]\n"
    "  stp x27, x28, [sp, #64]\n"
    "  stp x29, x30, [sp, #80]\n"
    "  stp d8, d9, [sp, #96]\n"
    "  stp d10, d11, [sp, #112]\n"
    "  stp d12, d13, [sp, #128]\n"
    "  stp d14, d15, [sp, #144]\n"
    "  mov x2, sp\n"
    "  str x2, [x0]\n"
    "  mov sp, x1\n"
    "  ldp x19, x20, [sp, #0]\n"
    "  ldp x21, x22, [sp, #16]\n"
    "  ldp x23, x24, [sp, #32]\n"
    "  ldp x25, x26, [sp, #48]\n"
    "  ldp x27, x28, [sp, #64]\n"
    "  ldp x29, x30, [sp, #80]\n"
    "  ldp d8, d9, [sp, #96]\n"
    "  ldp d10, d11, [sp, #112]\n"
    "  ldp d12, d13, [sp, #128]\n"
    "  ldp d14, d15, [sp, #144]\n"
    "  add sp, sp, #160\n"
    "  ret\n"
    ".size pythago_ctx_switch, .-pythago_ctx_switch\n"
    /* First switch into a VT lands here with the VT in x19. */
    ".p2align 2\n"
    ".type pythago_vt_trampoline, %function\n"
    "pythago_vt_trampoline:\n"
    "  mov x0, x19\n"
    "  bl pythago_vt_main\n"
    "  brk #0\n"
    ".size pythago_vt_trampoline, .-pythago_vt_trampoline\n"
    /* uintptr_t pythago_call_on_stack(void *top, fn, a0, a1, a2, a3): calls fn(a0..a3)
     * with sp = top, then returns on the original stack. */
    ".p2align 2\n"
    ".type pythago_call_on_stack, %function\n"
    "pythago_call_on_stack:\n"
    "  stp x29, x30, [sp, #-16]!\n"
    "  mov x29, sp\n"
    "  mov x9, sp\n"
    "  and x0, x0, #~15\n"
    "  mov sp, x0\n"
    "  str x9, [sp, #-16]!\n"
    "  mov x10, x1\n"
    "  mov x0, x2\n"
    "  mov x1, x3\n"
    "  mov x2, x4\n"
    "  mov x3, x5\n"
    "  blr x10\n"
    "  ldr x9, [sp], #16\n"
    "  mov sp, x9\n"
    "  ldp x29, x30, [sp], #16\n"
    "  ret\n"
    ".size pythago_call_on_stack, .-pythago_call_on_stack\n");

void pythago_ctx_switch(void **save_sp, void *load_sp);
void pythago_vt_trampoline(void);
uintptr_t pythago_call_on_stack(void *top, uintptr_t (*fn)(uintptr_t, uintptr_t, uintptr_t, uintptr_t),
                                uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3);

/* Go dispatcher for Python->Go calls (METH_FASTCALL|METH_KEYWORDS signature). */
static uintptr_t (*go_dispatch)(uintptr_t, uintptr_t, uintptr_t, uintptr_t);

void pythago_vt_set_go_dispatch(void *fn)
{
    go_dispatch = (uintptr_t (*)(uintptr_t, uintptr_t, uintptr_t, uintptr_t))fn;
}

/* Nested runs. A pinned VT (inside a Go callback) cannot yield, so where an unpinned VT
 * would park it blocks its carrier. That deadlocks when what it waits for needs another
 * VT of the same carrier: PyMutex wakes the oldest waiter first and hands it the mutex
 * after a millisecond, even a parked VT, so a same-carrier VT parked on the same mutex
 * (a shared dict's or list's critical section, say) takes the wake while the carrier is
 * stuck in the pinned wait.
 *
 * So a pinned VT that could otherwise park runs its carrier's runnable VTs itself, one at
 * a time, from inside its wait (vt__nest_one): the host scheduler's hook runs the next
 * runnable VT just as at top level, its context saved below the pinned frames. The hook
 * must consider every runnable VT of the carrier, including any it already took from the
 * C queue but has not run yet: the one the pinned VT waits for may be among them. Between
 * nested runs the pinned VT re-checks its own wait.
 *
 * Nested VTs never call Go. One that reaches a Go builtin detaches, parks, and is resumed
 * once the carrier is back at top level (deferred_go). Otherwise it would be pinned on top
 * and could block on what the buried VT holds; nested frames unwind last-in first-out, so
 * neither could go on, and every pinned VT blocked at once would take a stack level.
 * Nested VTs thus only park or block briefly, nesting is one level deep, and Go callbacks
 * are never re-entered from the scheduler.
 *
 * Known gap: a deferred VT keeps the Python locks it holds (a threading.Lock taken around
 * the Go call, an import's module lock) until the top level. A pinned VT waiting for one
 * of them still deadlocks the carrier. Releasing deferred VTs as a last resort, and letting
 * nested VTs call Go instead, were both tried: with many lock waiters pinned at once, the
 * stack fills before the holder runs. Closing the gap needs Go callbacks off the carrier
 * stack. */
static void vt_yield(pythago_vt *vt, int state);
static uintptr_t (*nest_run)(uintptr_t carrier); /* runs one VT; 0 if none was runnable */
void pythago_vt_set_nest_hook(void *fn) { nest_run = (uintptr_t (*)(uintptr_t))fn; }
static __thread int nest_depth;          /* this carrier is inside a nested run */
static __thread pythago_vt *deferred_go; /* VTs waiting for the top level to call Go */

/* Called by a VT's wait loop where it would park but cannot because it is pinned: runs one
 * runnable VT of its carrier, if there is one and nesting is possible. Returns whether it
 * ran one (the caller re-checks its wait either way). */
int vt__nest_one(pythago_vt *vt)
{
    if (nest_run == NULL || vt == NULL || !vt->pinned || nest_depth > 0 ||
        !vt__may_park_ex(vt, 1)) {
        return 0;
    }
    nest_depth++;
    int ran = nest_run((uintptr_t)vt->home) != 0;
    nest_depth--;
    return ran;
}

/* A pinned VT that can nest re-checks soon, to pick up VTs woken meanwhile. */
int64_t vt__block_slice(pythago_vt *vt)
{
    int64_t rc = vt__block_recheck_ns();
    if (vt != NULL && vt->pinned && nest_run != NULL && rc > 100000) {
        rc = 100000;
    }
    return rc;
}

/* ml_meth for Go-defined builtins. The Go runtime only accepts callbacks on the stack
 * of the thread it knows, so a VT calls Go on its carrier's stack, below the carrier's
 * saved context. A VT inside a Go callback is pinned: its frames are on the carrier
 * stack, so blocking calls block the carrier instead of yielding. */
PyObject *pythago_vt_fastcall(PyObject *self, PyObject *const *args, Py_ssize_t nargs,
                              PyObject *kwnames)
{
    pythago_vt *vt = vt_current();
    if (vt == NULL || vt->pinned) {
        return (PyObject *)go_dispatch((uintptr_t)self, (uintptr_t)args, (uintptr_t)nargs,
                                       (uintptr_t)kwnames);
    }
    while (nest_depth > 0) { /* nested: wait for the top level (see vt__nest_one) */
        PyThreadState *ts = PyEval_SaveThread();
        if (vt->defer_tok == 0) { /* not listed yet (an early wake may bring it back) */
            vt->next_defer = deferred_go;
            deferred_go = vt;
        }
        vt->defer_tok = vt__park_begin(vt);
        vt->wake_ns = -1; /* no deadline: only the top level wakes it */
        vt_yield(vt, VT_PARK);
        PyEval_RestoreThread(ts);
    }
    vt->pinned = 1;
    uintptr_t r = pythago_call_on_stack((char *)vt->carrier_sp - 64, go_dispatch, (uintptr_t)self,
                                        (uintptr_t)args, (uintptr_t)nargs, (uintptr_t)kwnames);
    vt->pinned = 0;
    return (PyObject *)r;
}

static void vt_yield(pythago_vt *vt, int state)
{
    /* errno belongs to the carrier's OS thread, which other VTs use meanwhile. */
    int saved_errno = errno;
    vt->state = state;
    pythago_ctx_switch(&vt->sp, vt->carrier_sp);
    vt->state = VT_RUNNING;
    errno = saved_errno;
}
void vt__yield(pythago_vt *vt, int state) { vt_yield(vt, state); }

static int vt_leak_tstate;
void pythago_vt_set_leak_tstate(int on) { vt_leak_tstate = on; }

#ifdef Py_GIL_DISABLED
#include "internal/pycore_brc.h"
#include "internal/pycore_interp.h"
#include "internal/pycore_llist.h"

/* Biased reference counting under M:N.
 *
 * An object's owner is the OS thread that allocated it (ob_tid = _Py_ThreadId(), the
 * carrier here); only the owner touches ob_ref_local, non-atomically. When another
 * thread's decref needs the two counts merged, CPython looks up the first thread state
 * registered for ob_tid. If that thread state is DETACHED, the other thread suspends it
 * and merges the counts itself (_Py_brc_queue_object, 3.14); if there is none, it
 * assumes the owner exited and merges too. Both are sound only if no thread state on
 * the owner's OS thread is attached meanwhile. With several VT thread states per
 * carrier, the one found could be a parked VT's while another VT runs on the carrier
 * and increments the same object locally: lost counts, objects freed while referenced.
 *
 * So each carrier keeps exactly one of its thread states in the table: the one about to
 * run (or none for a VT that has not bound its own yet; CPython adds that one when it
 * binds). Its ATTACHED/DETACHED state then tells the truth about the whole carrier.
 * Thread states taken off the table are left self-linked, so CPython's own removal at
 * thread-state teardown still works. Their queued objects are merged when they run
 * again (eval breaker), by the GC, or at teardown, all on the carrier or with the
 * world stopped.
 *
 * Call on the carrier with nothing attached. Waiting for the bucket mutex here is
 * bounded: no VT ever parks while waiting for a bucket mutex (vsem_wait), so its holder
 * is running elsewhere, inside a short critical section. */
static void carrier_brc_owner(PyThreadState *ts)
{
    uintptr_t tid = _Py_ThreadId();
    struct _brc_bucket *bucket = &PyInterpreterState_Main()->brc.table[tid % _Py_BRC_NUM_BUCKETS];
    struct llist_node *keep = NULL;
    if (ts != NULL && ((_PyThreadStateImpl *)ts)->brc.tid == tid) {
        keep = &((_PyThreadStateImpl *)ts)->brc.bucket_node;
    }
    PyMutex_Lock(&bucket->mutex);
    struct llist_node *node = bucket->root.next;
    while (node != &bucket->root) {
        struct llist_node *next = node->next;
        if (node != keep && llist_data(node, _PyThreadStateImpl, brc.bucket_node)->brc.tid == tid) {
            llist_remove(node);
            node->next = node->prev = node;
        }
        node = next;
    }
    if (keep != NULL && keep->next == keep) {
        llist_insert_tail(&bucket->root, keep);
    }
    PyMutex_Unlock(&bucket->mutex);
}
#else
static void carrier_brc_owner(PyThreadState *ts) { (void)ts; }
#endif

/* Thread states for VTs are pooled per carrier by Go, which owns the policy (sizing,
 * admission, trimming). C provides the mechanics: create a thread state on the carrier,
 * hand one to a VT before its first run, reset it when the VT ends, free it on the
 * carrier. A pooled thread state keeps its mimalloc heaps, which belong to the carrier's
 * OS thread where it is reused, so reuse skips both creation and the quadratic teardown
 * (the abandoned-segment walk). */

/* On the carrier, detached: a new thread state bound to this OS thread. */
PyThreadState *pythago_vt_tstate_new(void)
{
    Py_tss_t *key = &_PyRuntime.autoTSSkey;
    void *saved = PyThread_tss_get(key);
    PyThread_tss_set(key, NULL);
    PyThreadState *ts = PyThreadState_New(PyInterpreterState_Main());
    PyThread_tss_set(key, saved);
    return ts;
}

/* On the carrier that created ts, detached. */
void pythago_vt_tstate_free(PyThreadState *ts)
{
    Py_tss_t *key = &_PyRuntime.autoTSSkey;
    void *saved = PyThread_tss_get(key);
    PyThread_tss_set(key, ts);
    carrier_brc_owner(ts);
    PyEval_RestoreThread(ts);
    PyThreadState_Clear(ts);
    PyThreadState_DeleteCurrent();
    PyThread_tss_set(key, saved);
}

/* Gives a not-yet-started VT its thread state; it is returned (reset) when the VT ends. */
void pythago_vt_set_tstate(pythago_vt *vt, PyThreadState *ts)
{
    vt->tstate = ts;
    vt->pooled = 1;
}

int pythago_vt_is_raw(pythago_vt *vt) { return vt->fn != NULL; }

/* Clears what a new thread would not inherit. Runs attached, at VT exit. */
static void tstate_reset(PyThreadState *ts)
{
    _PyThreadStateImpl *impl = (_PyThreadStateImpl *)ts;
    /* Changing trace/profile functions stops the world in free-threaded builds; only
     * do it when the VT actually installed one. */
    if (ts->c_tracefunc != NULL) {
        PyEval_SetTrace(NULL, NULL);
    }
    if (ts->c_profilefunc != NULL) {
        PyEval_SetProfile(NULL, NULL);
    }
    Py_CLEAR(ts->dict);
    Py_CLEAR(ts->async_exc);
    Py_CLEAR(ts->async_gen_firstiter);
    Py_CLEAR(ts->async_gen_finalizer);
    Py_CLEAR(ts->context);
    ts->context_ver++;
    /* Dropping the sentinel runs the weakref callbacks that delete this thread's
     * threading.local dicts, as at thread exit. */
    Py_CLEAR(ts->threading_local_key);
    Py_CLEAR(ts->threading_local_sentinel);
    ts->coroutine_origin_tracking_depth = 0;
    Py_CLEAR(impl->asyncio_running_loop);
    Py_CLEAR(impl->asyncio_running_task);
}

/* Debug: reports leftovers that would leak into the next user of a pooled tstate. */
static void tstate_check(PyThreadState *ts, const char *where, pythago_vt *vt)
{
    _PyThreadStateImpl *impl = (_PyThreadStateImpl *)ts;
    if (ts->current_exception || ts->exc_info != &ts->exc_state || ts->exc_state.exc_value ||
        ts->current_frame || ts->critical_section || ts->delete_later || impl->c_stack_refs ||
        ts->tracing || ts->async_exc || (where[0] == 'r' && ts->state != 0 /* DETACHED */)) {
        fprintf(stderr, "pythago: tstate %p %s vt=%p: exc=%p exc_info=%s exc_value=%p frame=%p cs=%#lx "
                "delete_later=%p c_stack_refs=%p tracing=%d async_exc=%p state=%d\n",
                (void *)ts, where, (void *)vt, (void *)ts->current_exception,
                ts->exc_info == &ts->exc_state ? "base" : "nested", (void *)ts->exc_state.exc_value,
                (void *)ts->current_frame, (unsigned long)ts->critical_section,
                (void *)ts->delete_later, (void *)impl->c_stack_refs, ts->tracing,
                (void *)ts->async_exc, ts->state);
    }
}

__attribute__((used)) void pythago_vt_main(pythago_vt *vt)
{
    if (vt->fn != NULL) {
        /* A thread from pthread_create (threading.Thread): CPython's bootstrap creates,
         * binds and deletes its own thread state. */
        vt->fn(vt->fnarg);
        atomic_store(&vt->finished, 1);
        pythago_vtev_set(vt->done_ev);
        vt_yield(vt, VT_DONE);
        __builtin_trap();
    }
    PyThreadState *ts = vt->tstate;
    if (vt->pooled) {
        tstate_check(ts, "reuse", vt);
        ts->thread_id = (unsigned long)vt->ident; /* get_ident() for this VT */
        PyThread_tss_set(&_PyRuntime.autoTSSkey, ts);
    } else {
        /* Binds to pthread_self() (the VT id) and, because the carrier cleared the
         * GILState slot, makes this the GILState thread state while the VT runs. */
        ts = PyThreadState_New(PyInterpreterState_Main());
    }
    vt->tstate = ts;
    PyUnstable_ThreadState_SetStackProtection(ts, vt->stack, vt->stack_size);
    PyEval_RestoreThread(ts);
    PyObject *arg = PyLong_FromLongLong(vt->arg);
    PyObject *r = arg != NULL ? PyObject_CallOneArg(vt->callable, arg) : NULL;
    Py_XDECREF(arg);
    if (r == NULL) {
        PyErr_Print();
    }
    Py_XDECREF(r);
    Py_CLEAR(vt->callable);
    if (vt->pooled) {
        tstate_reset(ts);
        tstate_check(ts, "exit", vt);
        PyEval_SaveThread(); /* Go takes ts back from vt->tstate */
        vt_yield(vt, VT_DONE);
        __builtin_trap();
    }
    if (vt_leak_tstate) {
        PyEval_SaveThread(); /* Diagnostic: skip thread-state teardown. */
    } else {
        PyThreadState_Clear(ts);
        PyThreadState_DeleteCurrent();
    }
    vt->tstate = NULL;
    vt_yield(vt, VT_DONE);
    __builtin_trap(); /* A finished VT is never resumed. */
}

/* ---- the VT table ----
 * VT structs are never freed. A released VT goes back on a free list and its slot is
 * reused, with its generation bumped. Wakers, Go timers and thread handles hold tokens
 * (slot + generation or park sequence) or pointers to these type-stable structs, so a
 * late waker can at worst find a token that no longer matches; it never touches freed
 * memory or another allocation. */
#define VT_CHUNK_BITS 12
#define VT_CHUNK (1u << VT_CHUNK_BITS)
#define VT_GEN_MASK ((1ULL << 39) - 1)
static pythago_vt **vt_chunks[(1u << VT_IDX_BITS) / VT_CHUNK];
static _Atomic uint32_t vt_nslots;
static pythago_vt *vt_freelist; /* linked through next_ready */
static atomic_flag vt_table_lock = ATOMIC_FLAG_INIT;

static void table_lock(void)
{
    while (atomic_flag_test_and_set_explicit(&vt_table_lock, memory_order_acquire)) {
    }
}
static void table_unlock(void) { atomic_flag_clear_explicit(&vt_table_lock, memory_order_release); }

pythago_vt *vt__slot(uint32_t idx)
{
    if (idx >= atomic_load_explicit(&vt_nslots, memory_order_acquire)) {
        return NULL;
    }
    return vt_chunks[idx >> VT_CHUNK_BITS][idx & (VT_CHUNK - 1)];
}

static pthread_t ident_of(uint32_t idx, uint64_t gen)
{
    return (pthread_t)(((gen & VT_GEN_MASK) << (VT_IDX_BITS + 1)) | ((uint64_t)idx << 1) | 1);
}

pythago_vt *vt__by_ident(pthread_t th)
{
    if ((th & 1) == 0) {
        return NULL; /* a glibc thread */
    }
    pythago_vt *vt = vt__slot((uint32_t)((th >> 1) & ((1u << VT_IDX_BITS) - 1)));
    if (vt == NULL || (atomic_load(&vt->gen) & VT_GEN_MASK) != (uint64_t)th >> (VT_IDX_BITS + 1)) {
        return NULL; /* released (or never existed) */
    }
    return vt;
}

static pythago_vt *slot_get(void)
{
    table_lock();
    pythago_vt *vt = vt_freelist;
    if (vt != NULL) {
        vt_freelist = vt->next_ready;
        table_unlock();
        return vt;
    }
    uint32_t idx = atomic_load(&vt_nslots);
    if (idx > VT_IDX_MAX) {
        table_unlock();
        return NULL;
    }
    pythago_vt ***chunk = &vt_chunks[idx >> VT_CHUNK_BITS];
    if (*chunk == NULL && (*chunk = calloc(VT_CHUNK, sizeof(pythago_vt *))) == NULL) {
        table_unlock();
        return NULL;
    }
    vt = calloc(1, sizeof(*vt));
    if (vt != NULL) {
        vt->idx = idx;
        atomic_store(&vt->gen, 1);
        (*chunk)[idx & (VT_CHUNK - 1)] = vt;
        atomic_store_explicit(&vt_nslots, idx + 1, memory_order_release);
    }
    table_unlock();
    return vt;
}

static void slot_put(pythago_vt *vt)
{
    atomic_fetch_add(&vt->gen, 1); /* invalidates the ident before the slot is reused */
    table_lock();
    vt->next_ready = vt_freelist;
    vt_freelist = vt;
    table_unlock();
}

static pythago_vt *vt_alloc(size_t stack_size, int home)
{
    /* Reserve the stack with a guard page below; pages are committed on first touch. */
    char *mem = mmap(NULL, stack_size, PROT_READ | PROT_WRITE,
                     MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE | MAP_STACK, -1, 0);
    if (mem == MAP_FAILED) {
        return NULL;
    }
    pythago_vt *vt = slot_get();
    if (vt == NULL) {
        munmap(mem, stack_size);
        return NULL;
    }
    memset(&vt->VT_RESET_FROM, 0, sizeof(*vt) - offsetof(pythago_vt, VT_RESET_FROM));
    vt->ident = ident_of(vt->idx, atomic_load(&vt->gen));
    mprotect(mem, 4096, PROT_NONE);
    vt->stack = mem;
    vt->stack_size = stack_size;
    vt->home = home;
    vt->wake_ns = -1;
    /* Initial frame consumed by pythago_ctx_switch: x19 = vt, x30 = trampoline. */
    uintptr_t top = ((uintptr_t)mem + stack_size) & ~(uintptr_t)15;
    uintptr_t *frame = (uintptr_t *)(top - 160);
    memset(frame, 0, 160);
    frame[0] = (uintptr_t)vt;                     /* x19 */
    frame[10] = 0;                                /* x29 */
    frame[11] = (uintptr_t)pythago_vt_trampoline; /* x30 */
    vt->sp = frame;
    return vt;
}

/* pythago_vt_new prepares a VT that calls callable(arg). The caller must hold a
 * reference for the VT; it is released when the VT finishes. */
pythago_vt *pythago_vt_new(PyObject *callable, long long arg, size_t stack_size, int home)
{
    pythago_vt *vt = vt_alloc(stack_size, home);
    if (vt != NULL) {
        vt->callable = callable;
        vt->arg = arg;
    }
    return vt;
}

/* pythago_vt_run runs vt on the calling carrier thread until it blocks or finishes
 * and returns its state. The carrier must not have an attached thread state. */
int pythago_vt_run(pythago_vt *vt)
{
    Py_tss_t *key = &_PyRuntime.autoTSSkey;
    void *saved = PyThread_tss_get(key);
    pythago_vt *outer = vt_current(); /* the pinned VT, in a nested run */
    PyThread_tss_set(key, vt->tstate); /* NULL for a new VT, so it binds its own. */
    carrier_brc_owner(vt->tstate);     /* none for a new VT until CPython binds one */
    vt_set_current(vt);
    vt->state = VT_RUNNING;
    pythago_ctx_switch(&vt->carrier_sp, vt->sp);
    vt_set_current(outer);
    vt->tstate = PyThread_tss_get(key); /* includes thread states CPython bound itself */
    PyThread_tss_set(key, saved);
    int state = vt->state;
    if (outer != NULL) {
        carrier_brc_owner(saved); /* back to the pinned VT's thread state */
    } else {
        /* Back at top level: VTs that reached Go during a nested run may go on. */
        while (deferred_go != NULL) {
            pythago_vt *d = deferred_go;
            uint64_t tok = d->defer_tok;
            deferred_go = d->next_defer;
            d->defer_tok = 0;
            vt__wake_tok(tok);
        }
    }
    return state;
}

/* Nanoseconds until a sleeping VT should run again (<= 0: now). */
int64_t pythago_vt_sleep_remaining(pythago_vt *vt) { return vt->wake_ns - monotonic_ns(); }

void pythago_vt_free(pythago_vt *vt)
{
    munmap(vt->stack, vt->stack_size);
    vt->stack = NULL;
    slot_put(vt);
}

/* ---- threading.Thread on VTs ----
 * With spawning enabled, the glue's __wrap_pthread_create starts libpython's threads as
 * VTs: the pthread_t is the VT (as pthread_self() reports on it), the home carrier is
 * chosen round-robin, and the VT reaches Go through the ready list (no Go call on the
 * parent's stack). The handle stays valid until joined or detached. */
static int vt_spawn_carriers;
static size_t vt_spawn_stack = 1 << 20;
static _Atomic unsigned vt_spawn_rr;

void pythago_vt_set_threads(int carriers, size_t stack)
{
    if (stack != 0) {
        vt_spawn_stack = stack;
    }
    vt_spawn_carriers = carriers;
}

/* Returns -1 when spawning is off (the caller falls back), else 0 or an errno. */
int pythago_vt_spawn(void *(*fn)(void *), void *arg, size_t stack, pthread_t *out)
{
    if (vt_spawn_carriers <= 0) {
        return -1;
    }
    int home = (int)(atomic_fetch_add(&vt_spawn_rr, 1) % (unsigned)vt_spawn_carriers);
    pythago_vt *vt = vt_alloc(stack != 0 ? stack : vt_spawn_stack, home);
    if (vt == NULL) {
        return EAGAIN;
    }
    vt->fn = fn;
    vt->fnarg = arg;
    vt->kind = VT_KIND_PTHREAD;
    atomic_store(&vt->refs, 2);
    vt->done_ev = pythago_vtev_new();
    *out = vt->ident;
    vt__enqueue_new(vt);
    return 0;
}

int pythago_vt_is_spawned(pythago_vt *vt) { return vt->kind != VT_KIND_TASK; }

/* _pythago_vt.spawn(fn) -> ident. Runs fn(0) on a new VT with a pooled thread state, for
 * threading's _start_joinable_thread (installed by the bootstrap; no CPython changes).
 * Join and done-ness live in the Python-level handle. */
PyObject *pythago_vt_spawn_py(PyObject *self, PyObject *const *args, Py_ssize_t nargs)
{
    if (nargs != 1) {
        PyErr_SetString(PyExc_TypeError, "spawn(fn)");
        return NULL;
    }
    if (vt_spawn_carriers <= 0) {
        PyErr_SetString(PyExc_RuntimeError, "virtual threads are not running");
        return NULL;
    }
    size_t stack = PyThread_get_stacksize(); /* threading.stack_size(), 0 = default */
    int home = (int)(atomic_fetch_add(&vt_spawn_rr, 1) % (unsigned)vt_spawn_carriers);
    pythago_vt *vt = vt_alloc(stack != 0 ? stack : vt_spawn_stack, home);
    if (vt == NULL) {
        return PyErr_NoMemory();
    }
    vt->callable = Py_NewRef(args[0]);
    vt->kind = VT_KIND_PYTHON;
    atomic_store(&vt->refs, 1);
    PyObject *ident = PyLong_FromUnsignedLong(vt->ident);
    vt__enqueue_new(vt);
    return ident;
}

/* Drops one reference (the finished thread's, or its handle's). */
void pythago_vt_release(pythago_vt *vt)
{
    if (atomic_fetch_sub(&vt->refs, 1) != 1) {
        return;
    }
    free(vt->done_ev);
    pythago_vt_free(vt);
}

/* A pthread_create'd VT's handle; NULL for glibc threads and released VTs. */
static pythago_vt *vt_handle(pthread_t th)
{
    pythago_vt *vt = vt__by_ident(th);
    return vt != NULL && vt->kind == VT_KIND_PTHREAD ? vt : NULL;
}

/* pthread_join for VT handles: -1 if th is a glibc thread, else 0 or an errno (ESRCH for
 * a handle already joined or detached). Parks a joining VT. */
int pythago_vt_join_handle(pthread_t th)
{
    if ((th & 1) == 0) {
        return -1;
    }
    pythago_vt *vt = vt_handle(th);
    if (vt == NULL) {
        return ESRCH;
    }
    while (!atomic_load(&vt->finished)) {
        uint32_t seq = vt__ev_seq(vt->done_ev);
        if (atomic_load(&vt->finished)) {
            break;
        }
        vt__ev_wait(vt->done_ev, seq, -1);
    }
    pythago_vt_release(vt);
    return 0;
}

int pythago_vt_detach_handle(pthread_t th)
{
    if ((th & 1) == 0) {
        return -1;
    }
    pythago_vt *vt = vt_handle(th);
    if (vt == NULL) {
        return ESRCH;
    }
    pythago_vt_release(vt);
    return 0;
}

/* CPython hangs threads that must not run again (daemon threads at shutdown) with
 * pause() or pthread_exit(); a VT instead leaves its carrier for good. */
int pythago_vt_hang_current(void)
{
    pythago_vt *vt = vt_current();
    if (vt == NULL || vt->pinned) {
        return 0;
    }
    if (PyThreadState_GetUnchecked() != NULL) {
        PyThreadState_Swap(NULL);
    }
    for (;;) {
        vt_yield(vt, VT_HUNG);
    }
}

int __wrap_pause(void)
{
    pythago_vt_hang_current();
    return pause();
}

/* libpython passes pthread_self() results to these; on a VT that is a VT ident, not a
 * glibc thread, so they must not reach glibc. Odd values are always VT idents (glibc's
 * pthread_t is an aligned pointer); a released one is ESRCH. */
static pythago_vt *vt_of(pthread_t th)
{
    pythago_vt *cur = vt_current();
    return cur != NULL && th == cur->ident ? cur : vt__by_ident(th);
}

int __wrap_pthread_setname_np(pthread_t th, const char *name)
{
    pythago_vt *vt = vt_of(th);
    if (vt == NULL) {
        return (th & 1) ? ESRCH : pthread_setname_np(th, name);
    }
    strncpy(vt->name, name, sizeof(vt->name) - 1);
    return 0;
}

int __wrap_pthread_getname_np(pthread_t th, char *buf, size_t len)
{
    pythago_vt *vt = vt_of(th);
    if (vt == NULL) {
        return (th & 1) ? ESRCH : pthread_getname_np(th, buf, len);
    }
    if (len == 0) {
        return ERANGE;
    }
    strncpy(buf, vt->name, len - 1);
    buf[len - 1] = 0;
    return 0;
}

int __wrap_pthread_getcpuclockid(pthread_t th, clockid_t *clock)
{
    /* A VT has no CPU clock of its own (its carrier runs many VTs). */
    if (th & 1) {
        return vt_of(th) != NULL ? ENOTSUP : ESRCH;
    }
    return pthread_getcpuclockid(th, clock);
}

int __wrap_pthread_kill(pthread_t th, int sig)
{
    if ((th & 1) == 0) {
        return pthread_kill(th, sig);
    }
    if (vt_of(th) == NULL) {
        return ESRCH;
    }
    return sig == 0 ? 0 : ENOTSUP; /* existence check only */
}

int __wrap_clock_nanosleep(clockid_t clock, int flags, const struct timespec *req,
                           struct timespec *rem)
{
    pythago_vt *vt = vt_current();
    if (vt == NULL || clock != CLOCK_MONOTONIC) {
        return clock_nanosleep(clock, flags, req, rem);
    }
    int64_t ns = (int64_t)req->tv_sec * 1000000000 + req->tv_nsec;
    int64_t deadline = (flags & TIMER_ABSTIME) ? ns : monotonic_ns() + ns;
    /* A VT that may not park right now (a stop-the-world is pending, ...) sleeps in short
     * slices and re-checks, rather than holding its carrier for the whole sleep. */
    for (;;) {
        if (vt__may_park(vt)) {
            vt->wake_ns = deadline;
            vt_yield(vt, VT_SLEEP);
            return 0;
        }
        int64_t now = monotonic_ns();
        if (now >= deadline) {
            return 0;
        }
        if (vt__nest_one(vt)) {
            continue; /* pinned: ran a VT of this carrier instead of sleeping */
        }
        int64_t d = deadline - now, rc = vt__block_slice(vt);
        d = d > rc ? rc : d;
        struct timespec rel = {.tv_sec = d / 1000000000, .tv_nsec = d % 1000000000};
        int r = clock_nanosleep(CLOCK_MONOTONIC, 0, &rel, NULL);
        if (r != 0) {
            if (r == EINTR && rem != NULL && !(flags & TIMER_ABSTIME)) {
                int64_t left = deadline - monotonic_ns();
                left = left > 0 ? left : 0;
                *rem = (struct timespec){.tv_sec = left / 1000000000, .tv_nsec = left % 1000000000};
            }
            return r;
        }
    }
}
#else
int __wrap_clock_nanosleep(clockid_t clock, int flags, const struct timespec *req,
                           struct timespec *rem)
{
    return clock_nanosleep(clock, flags, req, rem);
}
int pythago_vt_spawn(void *(*fn)(void *), void *arg, size_t stack, pthread_t *out) { return -1; }
int pythago_vt_join_handle(pthread_t th) { return -1; }
int pythago_vt_detach_handle(pthread_t th) { return -1; }
int pythago_vt_hang_current(void) { return 0; }
int __wrap_pause(void) { return pause(); }
int __wrap_pthread_setname_np(pthread_t th, const char *name) { return pthread_setname_np(th, name); }
int __wrap_pthread_getname_np(pthread_t th, char *buf, size_t len) { return pthread_getname_np(th, buf, len); }
int __wrap_pthread_getcpuclockid(pthread_t th, clockid_t *c) { return pthread_getcpuclockid(th, c); }
int __wrap_pthread_kill(pthread_t th, int sig) { return pthread_kill(th, sig); }
#endif

#ifdef PYTHAGO_VT
#include <signal.h>
#include <ucontext.h>
/* Profiling aid: a sampling handler for a spare signal that Go sends to carriers.
 * Each sample is stored as [depth, pc, lr, frame-pointer return addresses...]; the walk
 * is bounded to the running VT stack. */
#define PROF_MAX (1 << 22)
static uintptr_t prof_buf[PROF_MAX];
static size_t prof_n;

static void prof_handler(int sig, siginfo_t *si, void *ucv)
{
    ucontext_t *uc = ucv;
    uintptr_t st[40];
    size_t d = 0;
    st[d++] = uc->uc_mcontext.pc;
    st[d++] = uc->uc_mcontext.regs[30];
    pythago_vt *vt = current_vt;
    uintptr_t lo = vt ? (uintptr_t)vt->stack : 0, hi = vt ? lo + vt->stack_size : 0;
    uintptr_t fp = uc->uc_mcontext.regs[29];
    while (d < 40 && fp >= lo && fp + 16 <= hi && (fp & 15) == 0) {
        uintptr_t *f = (uintptr_t *)fp;
        st[d++] = f[1];
        if (f[0] <= fp) {
            break;
        }
        fp = f[0];
    }
    size_t at = __atomic_fetch_add(&prof_n, d + 1, __ATOMIC_RELAXED);
    if (at + d + 1 > PROF_MAX) {
        return;
    }
    prof_buf[at] = d;
    memcpy(&prof_buf[at + 1], st, d * sizeof(uintptr_t));
}

int pythago_prof_install(int sig)
{
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_sigaction = prof_handler;
    sa.sa_flags = SA_SIGINFO | SA_ONSTACK | SA_RESTART;
    sigemptyset(&sa.sa_mask);
    return sigaction(sig, &sa, NULL);
}

uintptr_t *pythago_prof_buf(size_t *n)
{
    *n = __atomic_load_n(&prof_n, __ATOMIC_RELAXED);
    return prof_buf;
}
#endif

#ifdef PYTHAGO_VT
/* Whether vt has run before (its tstate or a suspended context exists). */
int pythago_vt_started(pythago_vt *vt) { return vt->tstate != NULL || vt->state != VT_NEW; }
#endif

#ifdef PYTHAGO_VT
#include "internal/pycore_interp_structs.h"
/* Diagnostics: the main interpreter's and the runtime's stop-the-world state.
 * out = {requested, world_stopped, countdown, requester, global requested, global stopped,
 *        global countdown, global requester} */
void pythago_vt_stw_info(uintptr_t *out)
{
    struct _stoptheworld_state *s = &PyInterpreterState_Main()->stoptheworld;
    struct _stoptheworld_state *g = &_PyRuntime.stoptheworld;
    out[0] = s->requested, out[1] = s->world_stopped, out[2] = (uintptr_t)s->thread_countdown;
    out[3] = (uintptr_t)s->requester;
    out[4] = g->requested, out[5] = g->world_stopped, out[6] = (uintptr_t)g->thread_countdown;
    out[7] = (uintptr_t)g->requester;
}

uintptr_t pythago_vt_tstate(pythago_vt *vt) { return (uintptr_t)vt->tstate; }
#endif

#ifdef PYTHAGO_VT
#include <stdio.h>
#include "internal/pycore_interpframe.h"
/* Diagnostics: "qualname:line <- ..." for vt's Python frames, innermost first, plus
 * whether it is on the deferred-Go list. Racy reads; for hung processes only. */
size_t pythago_vt_pyframes(pythago_vt *vt, char *buf, size_t n)
{
    size_t k = (size_t)snprintf(buf, n, "[c%d%s%s] ", vt->home, vt->pinned ? " pinned" : "",
                                vt->defer_tok ? " deferred" : "");
    _PyInterpreterFrame *f = vt->tstate ? vt->tstate->current_frame : NULL;
    for (int d = 0; f != NULL && d < 16 && k + 1 < n; d++, f = f->previous) {
        PyCodeObject *co = _PyFrame_SafeGetCode(f);
        if (co == NULL) {
            continue;
        }
        const char *name = PyUnicode_IS_COMPACT_ASCII(co->co_qualname)
            ? (const char *)PyUnicode_DATA(co->co_qualname) : "?";
        k += (size_t)snprintf(buf + k, n - k, "%s%s:%d", d ? " <- " : "", name,
                              PyUnstable_InterpreterFrame_GetLine(f));
    }
    return k < n ? k : n - 1;
}
#endif

#ifdef PYTHAGO_VT
/* Diagnostics: return addresses of a suspended VT, from its saved context (x29/x30 at
 * offsets 80/88 of the frame pythago_ctx_switch pushed), bounded to its stack. */
size_t pythago_vt_backtrace(pythago_vt *vt, uintptr_t *out, size_t max)
{
    if (vt->state == VT_RUNNING || vt->state == VT_NEW || vt->sp == NULL) {
        return 0;
    }
    uintptr_t *ctx = vt->sp, lo = (uintptr_t)vt->stack, hi = lo + vt->stack_size;
    uintptr_t fp = ctx[10];
    size_t n = 0;
    out[n++] = ctx[11];
    while (n < max && fp >= lo && fp + 16 <= hi && (fp & 15) == 0) {
        uintptr_t *f = (uintptr_t *)fp;
        out[n++] = f[1];
        if (f[0] <= fp) {
            break;
        }
        fp = f[0];
    }
    return n;
}
#endif

