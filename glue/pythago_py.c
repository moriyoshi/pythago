/*
 * Support for package py: Python threads backed by goroutines, and goroutines entering
 * Python.
 *
 * Invariant: an OS thread has at most one Python thread state. Free-threaded CPython keys
 * object ownership (ob_tid, biased reference counts) on the OS thread, and 3.14 merges a
 * detached owner's refcounts on its behalf (_Py_brc_queue_object). A second, detached
 * thread state on a thread that is running Python would let that merge race the owner's
 * non-atomic fast path. So the thread state py caches per OS thread is dropped before a
 * Python thread starts on that OS thread, and freed when the thread exits.
 */
#include <Python.h>
#ifdef _WIN32
#include "pythago_win32.h"
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#else
#include <errno.h>
#include <limits.h>
#include <pthread.h>
#include <sched.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#ifdef __APPLE__
#include <stdint.h>
#else
#include <linux/futex.h>
#include <sys/syscall.h>
#endif
#endif

/* Counters Go reads as 64-bit. long is 32-bit on Windows; elsewhere the type stays long,
 * so the Linux and darwin objects do not change (glue/prebuilt). */
#ifdef _WIN32
typedef long long pythago_long;
#else
typedef long pythago_long;
#endif

static __thread int depth;            /* >0: this OS thread is inside Python on Go's behalf */
static __thread PyThreadState *owned; /* the thread state py created for this OS thread */
static __thread int slot_held;        /* an outer py.Do on this thread holds an admission slot */
static pthread_key_t owned_key;       /* its destructor frees owned at thread exit */
static pthread_once_t owned_once = PTHREAD_ONCE_INIT;
static atomic_int alive;              /* initialized and not finalizing */
static atomic_int deleting;           /* threads inside owned_drop */
static _Atomic(pythago_long) owned_created, owned_deleted;

static void owned_delete(PyThreadState *ts)
{
    atomic_fetch_add(&deleting, 1);
    /* alive is cleared before py.Finalize finalizes, but the command line (py.Main:
     * Py_RunMain) finalizes on its own: attaching a thread state then hangs (3.14), as it
     * did when the fusion's startup check thread's slot destructor ran at ExitProcess
     * (Windows runs the exiting thread's slot destructors there). */
    if (ts != NULL && atomic_load(&alive) && Py_IsInitialized() && !Py_IsFinalizing()) {
        PyEval_RestoreThread(ts);
        PyThreadState_Clear(ts);
        PyThreadState_DeleteCurrent();
        atomic_fetch_add(&owned_deleted, 1);
    }
    atomic_fetch_sub(&deleting, 1);
}

static void owned_drop(void)
{
    PyThreadState *ts = owned;
    owned = NULL;
    owned_delete(ts);
}

/* At thread exit the key's value is the thread state. The __thread variables may be gone
 * by then: on darwin another key destructor releases the TLV storage, in no fixed order,
 * and owned reads as NULL there. So the destructor uses only its argument. */
static void owned_dtor(void *p) { owned_delete(p); }

static void owned_key_init(void) { pthread_key_create(&owned_key, owned_dtor); }

/* pythago_py_started marks the runtime usable, after initialization. */
static void spares_refill(void);
static void spares_stop(void);
static void forked_child(void);

#if !defined(__APPLE__) && !defined(_WIN32)
/* glibc keeps pthread_atfork in the static libc_nonshared.a; libc.so.6 exports
 * __register_atfork, which takes the image's __dso_handle (as atexit does). */
extern int __register_atfork(void (*prepare)(void), void (*parent)(void), void (*child)(void),
                             void *dso);
extern char __dso_handle;
#endif

void pythago_py_started(void)
{
    pthread_once(&owned_once, owned_key_init);
#if defined(_WIN32)
    (void)forked_child; /* no fork */
#elif defined(__APPLE__)
    pthread_atfork(NULL, NULL, forked_child);
#else
    __register_atfork(NULL, NULL, forked_child, &__dso_handle);
#endif
    atomic_store(&alive, 1);
    spares_refill();
}

/* pythago_py_stopping runs before Py_FinalizeEx, which deletes every thread state: from
 * then on exiting threads leave their cached thread state alone. */
void pythago_py_stopping(void)
{
    spares_stop();
    atomic_store(&alive, 0);
    while (atomic_load(&deleting) != 0) {
        sched_yield();
    }
}

int pythago_py_depth(void) { return depth; }

/* pythago_py_enter attaches this OS thread's thread state, creating one if it has none.
 * slot says whether the caller (an outer py.Do) holds an admission slot. It returns 1
 * when it attached (pythago_py_leave(1, slot) detaches again), 0 when a thread state was
 * already attached, and -1 if none could be created. */
int pythago_py_enter(int slot)
{
    if (PyThreadState_GetUnchecked() != NULL) {
        depth++;
        return 0;
    }
    PyThreadState *ts = PyGILState_GetThisThreadState();
    if (ts == NULL) {
        /* Init enters Python on other OS threads before pythago_py_started (the fusion's
         * startup check): without the key yet, owned_key is 0, and key 0 is the native
         * image's TLS key, whose value the thread state would then replace. */
        pthread_once(&owned_once, owned_key_init);
        ts = PyThreadState_New(PyInterpreterState_Main()); /* binds the GILState slot */
        if (ts == NULL) {
            return -1;
        }
        owned = ts;
        pthread_setspecific(owned_key, ts);
        atomic_fetch_add(&owned_created, 1);
    }
    PyEval_RestoreThread(ts);
    depth++;
    slot_held |= slot;
    return 1;
}

void pythago_py_leave(int attached, int slot)
{
    depth--;
    if (slot) {
        slot_held = 0;
    }
    if (attached == 1) {
        PyEval_SaveThread();
    }
}

/* Detach around blocking Go code; NULL when nothing is attached. *slot reports (and
 * hands to the caller) the admission slot this thread holds. */
PyThreadState *pythago_py_release(int *slot)
{
    *slot = slot_held;
    slot_held = 0;
    return PyThreadState_GetUnchecked() != NULL ? PyEval_SaveThread() : NULL;
}

void pythago_py_reacquire(PyThreadState *ts, int slot)
{
    slot_held = slot;
    if (ts != NULL) {
        PyEval_RestoreThread(ts);
    }
}

/*
 * Goroutine-backed Python threads. The whole handshake is in C, so no Go code blocks
 * inside a callback:
 *   - pthread_create (pythago_py_spawn) hands the thread to a spare: a goroutine already
 *     locked to its own OS thread and waiting in C. Without a spare it calls the Go spawn
 *     hook, which starts such a goroutine and returns. Either way it then waits on a futex
 *     for the child's pthread_t. Go creating an OS thread for a locked goroutine takes
 *     about 50 us, which spares move off the critical path; a spare asks Go for its
 *     replacement once it has a thread to run;
 *   - the goroutine runs pythago_py_thread_main, which publishes pthread_self, runs
 *     CPython's thread function, and waits until the thread is joined or detached before
 *     returning: the goroutine, and its OS thread, then exit, so every Python thread gets
 *     a fresh OS thread and keeps its pthread_t until joined or detached, as with libc;
 *   - pthread_join and pthread_detach find the record by pthread_t, entirely in C.
 */
typedef struct pythread {
    void *(*fn)(void *);
    void *arg;
    pthread_t self;
    atomic_uint started, done, released; /* futex flags, 0 -> 1 */
    struct pythread *next;               /* registry chain */
} pythread;

/* A spare waiting for a thread to run; it lives on the spare's own stack. */
typedef struct spare {
    atomic_uint assigned;
    pythread *t; /* NULL: exit */
    struct spare *next;
} spare;

static void (*go_spawn)(pythread *); /* start a goroutine running pythago_py_thread_main(t) */
static __thread pythread *current;   /* this OS thread's record, if goroutine-backed */
static _Atomic(pythago_long) live, limit = 4096, started_count;

#define NBUCKETS 256
static pythread *registry[NBUCKETS];
static pthread_mutex_t registry_mu = PTHREAD_MUTEX_INITIALIZER;

static pthread_mutex_t spare_mu = PTHREAD_MUTEX_INITIALIZER;
static spare *spares;
static int nspares, spares_pending, spares_target, spares_stopped;

/* fn is the Go spawn hook; target is how many spares to keep. */
void pythago_py_set_spawn(void *fn, int target)
{
    go_spawn = (void (*)(pythread *))fn;
    spares_target = target;
}

pythago_long pythago_py_set_limit(pythago_long n) { return atomic_exchange(&limit, n); }

static size_t bucket_of(pthread_t th)
{
    return (size_t)(((uintptr_t)th >> 4) * 0x9E3779B97F4A7C15ull >> 56) % NBUCKETS;
}

#if defined(_WIN32)
/* WaitOnAddress and WakeByAddress* (Windows 8+) are the futex: they compare the word
 * with v before sleeping, and waking an address nobody waits on does nothing. */
static void sys_wait(atomic_uint *w, unsigned v)
{
    WaitOnAddress((volatile void *)w, &v, sizeof v, INFINITE);
}

static void sys_wake(atomic_uint *w, int all)
{
    if (all) {
        WakeByAddressAll((void *)w);
    } else {
        WakeByAddressSingle((void *)w);
    }
}
#elif defined(__APPLE__)
/* darwin has no futex. __ulock_wait/__ulock_wake (libsystem_kernel, macOS 10.12+) are the
 * same primitive: libc++'s atomic wait uses them. Waking an address nobody waits on, or
 * memory already freed, only returns ENOENT. */
#define UL_COMPARE_AND_WAIT 1
#define ULF_WAKE_ALL 0x00000100
#define ULF_NO_ERRNO 0x01000000
extern int __ulock_wait(uint32_t op, void *addr, uint64_t value, uint32_t timeout_us);
extern int __ulock_wake(uint32_t op, void *addr, uint64_t wake_value);

static void sys_wait(atomic_uint *w, unsigned v)
{
    __ulock_wait(UL_COMPARE_AND_WAIT | ULF_NO_ERRNO, w, v, 0);
}

static void sys_wake(atomic_uint *w, int all)
{
    __ulock_wake(UL_COMPARE_AND_WAIT | ULF_NO_ERRNO | (all ? ULF_WAKE_ALL : 0), w, 0);
}
#else
static void sys_wait(atomic_uint *w, unsigned v)
{
    syscall(SYS_futex, w, FUTEX_WAIT_PRIVATE, v, NULL, NULL, 0);
}

static void sys_wake(atomic_uint *w, int all)
{
    syscall(SYS_futex, w, FUTEX_WAKE_PRIVATE, all ? INT_MAX : 1, NULL, NULL, 0);
}
#endif

static void futex_wait(atomic_uint *w, unsigned v)
{
    int e = errno;
    sys_wait(w, v);
    errno = e;
}

static void await_flag(atomic_uint *w)
{
    while (atomic_load(w) == 0) {
        futex_wait(w, 0);
    }
}

/* The waiter may free the flag's record as soon as it sees the store, so the wake can hit
 * freed memory; FUTEX_WAKE there is harmless (at worst a spurious wakeup elsewhere). */
static void set_flag(atomic_uint *w)
{
    int e = errno;
    atomic_store(w, 1);
    sys_wake(w, 1);
    errno = e;
}

static pythread *registry_take(pthread_t th)
{
    pthread_mutex_lock(&registry_mu);
    pythread **pp = &registry[bucket_of(th)];
    while (*pp != NULL && !pthread_equal((*pp)->self, th)) {
        pp = &(*pp)->next;
    }
    pythread *t = *pp;
    if (t != NULL) {
        *pp = t->next;
    }
    pthread_mutex_unlock(&registry_mu);
    return t;
}

/* Ask Go for spares until spares_target are waiting or on their way. */
static void spares_refill(void)
{
    for (;;) {
        pthread_mutex_lock(&spare_mu);
        int need = !spares_stopped && nspares + spares_pending < spares_target;
        spares_pending += need;
        pthread_mutex_unlock(&spare_mu);
        if (!need) {
            return;
        }
        go_spawn(NULL);
    }
}

/* Wake every waiting spare to exit (at finalization). */
static void spares_stop(void)
{
    pthread_mutex_lock(&spare_mu);
    spares_stopped = 1;
    spare *s = spares;
    spares = NULL;
    nspares = 0;
    pthread_mutex_unlock(&spare_mu);
    while (s != NULL) {
        spare *next = s->next; /* s is gone once assigned */
        s->t = NULL;
        set_flag(&s->assigned);
        s = next;
    }
}

/* pythago_py_spawn starts fn(arg) on a goroutine-backed thread. It returns -1 when py is
 * not installed, else 0 or EAGAIN like pthread_create. */
int pythago_py_spawn(void *(*fn)(void *), void *arg, pthread_t *out)
{
    if (go_spawn == NULL) {
        return -1;
    }
    if (atomic_fetch_add(&live, 1) >= atomic_load(&limit)) {
        atomic_fetch_sub(&live, 1);
        return EAGAIN;
    }
    pythread *t = calloc(1, sizeof(*t));
    if (t == NULL) {
        atomic_fetch_sub(&live, 1);
        return EAGAIN;
    }
    t->fn = fn;
    t->arg = arg;
    pthread_mutex_lock(&spare_mu);
    spare *s = spares;
    if (s != NULL) {
        spares = s->next;
        nspares--;
    }
    pthread_mutex_unlock(&spare_mu);
    if (s != NULL) {
        s->t = t;
        set_flag(&s->assigned);
    } else {
        go_spawn(t);
    }
    await_flag(&t->started);
    *out = t->self; /* t lives until the thread is joined or detached */
    return 0;
}

/* pythago_py_join and pythago_py_detach return -1 for threads py did not start. */
int pythago_py_join(pthread_t th)
{
    pythread *t = registry_take(th);
    if (t == NULL) {
        return -1;
    }
    await_flag(&t->done);
    set_flag(&t->released);
    return 0;
}

int pythago_py_detach(pthread_t th)
{
    pythread *t = registry_take(th);
    if (t == NULL) {
        return -1;
    }
    set_flag(&t->released);
    return 0;
}

/* pthread_exit (daemon threads at finalization) would force-unwind through Go frames, so
 * a goroutine-backed thread parks forever instead. Returns for other threads. */
void pythago_py_exit_current(void)
{
    if (current == NULL) {
        return;
    }
    atomic_uint never = 0;
    for (;;) {
        futex_wait(&never, 0);
    }
}

/*
 * fork() without exec (os.fork, multiprocessing "fork"): the child has only the forking
 * thread, and the Go runtime cannot run there. Forget the parent's spares and thread
 * records, start threads with libc from now on, and make Go functions raise instead of
 * entering Go.
 */
static atomic_int in_forked_child;

static void forked_child(void)
{
    atomic_store(&in_forked_child, 1);
    go_spawn = NULL; /* pythago_py_spawn returns -1: libc creates threads */
    pthread_mutex_init(&registry_mu, NULL);
    pthread_mutex_init(&spare_mu, NULL);
    for (size_t i = 0; i < NBUCKETS; i++) {
        registry[i] = NULL; /* the parent's threads; their records are not ours to free */
    }
    spares = NULL;
    nspares = spares_pending = 0;
    spares_stopped = 1;
    current = NULL; /* the forking thread is the child's main thread now */
}

/* pythago_py_thread_main runs on a spawned goroutine, locked to its OS thread. t is the
 * thread to run, or NULL for a spare, which waits for one. */
void pythago_py_thread_main(pythread *t)
{
    if (t == NULL) {
        spare s = {0};
        pthread_mutex_lock(&spare_mu);
        spares_pending--;
        if (spares_stopped) {
            pthread_mutex_unlock(&spare_mu);
            return;
        }
        s.next = spares;
        spares = &s;
        nspares++;
        pthread_mutex_unlock(&spare_mu);
        await_flag(&s.assigned);
        if ((t = s.t) == NULL) {
            return;
        }
    }
    t->self = pthread_self();
    current = t;
    pthread_mutex_lock(&registry_mu);
    size_t b = bucket_of(t->self);
    t->next = registry[b];
    registry[b] = t;
    pthread_mutex_unlock(&registry_mu);
    atomic_fetch_add(&started_count, 1);
    set_flag(&t->started);
    spares_refill(); /* replace this spare, now that the parent is running again */
    if (owned != NULL) {
        /* The invariant above: give up the thread state Do cached on this OS thread
         * before CPython binds the new thread's own. */
        owned_drop();
        pthread_setspecific(owned_key, NULL);
    }
    depth = 1;
    t->fn(t->arg);
    depth = 0;
    current = NULL;
    set_flag(&t->done);
    await_flag(&t->released);
    free(t);
    atomic_fetch_sub(&live, 1);
}

/* A Python value described for, or by, Go: an item for pythago_py_build, or a decoded
 * argument of a Go function. */
struct pythago_value { /* py/convert.go pyValue */
    uint64_t tag;
    uint64_t a; /* PV_OBJECT, PV_BORROWED: the PyObject*; PV_INT, PV_UINT, PV_FLOAT: the
                 * bits; PV_STR, PV_BYTES: the payload's offset in the arena (build), or
                 * its address (a decoded argument) */
    uint64_t b; /* PV_STR, PV_BYTES: the payload's length */
};

enum {
    PV_OBJECT, /* build: a new reference, which the builder consumes (also on failure);
                * a decoded argument: not decoded */
    PV_NONE,
    PV_TRUE,
    PV_FALSE,
    PV_INT,
    PV_UINT,
    PV_FLOAT,
    PV_STR, /* UTF-8 */
    PV_BYTES,
    PV_BORROWED, /* a borrowed reference: the container takes a new one */
};

/* Every Go-defined builtin has ml_meth = pythago_py_fastcall (METH_FASTCALL |
 * METH_KEYWORDS) and, as self, its index in Go's function table. The call is decoded
 * here and handed to the single Go dispatcher through crosscall2, the entry cgo-exported
 * functions use, so that Go needs no reflection to read it and no further C calls for
 * the index, the keyword count or a borrowed result's new reference. The OS thread is
 * marked as inside Python while the dispatcher runs. */
/* The layout of the structures shared with Go (struct pythago_call, struct
 * pythago_value, and the fusion's struct pythago_fusion_config and pythago_fusion_cell).
 * py/capi.go checks it at Init, so a stale native image fails clearly instead of
 * misreading memory; change both together. 4: every argument and keyword name is
 * decoded, into memory passed by pointer. 5: the fusion (Go-owned bytes). */
#define PYTHAGO_PY_ABI 5

int pythago_py_abi(void)
{
    return PYTHAGO_PY_ABI;
}

/* How many values (arguments plus keyword names) a call decodes into an array on the C
 * stack; larger calls use PyMem_Malloc. Internal to the glue: Go reads the arrays through
 * the pointers in struct pythago_call, whatever their size. */
#define PYTHAGO_STACK_VALUES 24

struct pythago_call { /* py/func.go callFrame */
    PyObject *const *args;
    Py_ssize_t nargs;
    PyObject *kwnames;
    Py_ssize_t nkw;
    Py_ssize_t index;
    PyObject *result; /* NULL with an exception set on failure */
    Py_ssize_t borrowed; /* result is borrowed: take a new reference to it */
    /* Every argument (positional, then keyword: nargs + nkw values) and every keyword name
     * (nkw values), decoded here so that Go reads scalars and names without calling back
     * into C. Both point into memory that lives until the call returns. */
    struct pythago_value *decoded;
    struct pythago_value *names;
};

/* decode describes o if reading it has no side effects and cannot fail: None, bools,
 * exact ints that fit in 64 bits, exact floats, exact bytes and exact ASCII str (whose
 * UTF-8 form is its storage). Anything else is left PV_OBJECT: Go uses the C API. */
static void decode(PyObject *o, struct pythago_value *v)
{
    v->tag = PV_OBJECT;
    if (o == Py_None) {
        v->tag = PV_NONE;
    } else if (o == Py_True) {
        v->tag = PV_TRUE;
    } else if (o == Py_False) {
        v->tag = PV_FALSE;
    } else if (PyLong_CheckExact(o)) {
        int overflow;
        long long n = PyLong_AsLongLongAndOverflow(o, &overflow);
        if (overflow == 0) {
            v->tag = PV_INT;
            v->a = (uint64_t)n;
        }
    } else if (PyFloat_CheckExact(o)) {
        double d = PyFloat_AS_DOUBLE(o);
        v->tag = PV_FLOAT;
        memcpy(&v->a, &d, sizeof d);
    } else if (PyBytes_CheckExact(o)) {
        v->tag = PV_BYTES;
        v->a = (uint64_t)(uintptr_t)PyBytes_AS_STRING(o);
        v->b = (uint64_t)PyBytes_GET_SIZE(o);
    } else if (PyUnicode_CheckExact(o) && PyUnicode_IS_ASCII(o)) {
        Py_ssize_t n;
        const char *s = PyUnicode_AsUTF8AndSize(o, &n); /* the ASCII storage: no copy */
        if (s != NULL) {
            v->tag = PV_STR;
            v->a = (uint64_t)(uintptr_t)s;
            v->b = (uint64_t)n;
        }
    }
}

static void (*go_crosscall2)(void (*)(void *), void *, int, uintptr_t);
static void (*go_dispatch)(void *);

void pythago_py_set_callback(void *crosscall2, void *dispatch)
{
    go_crosscall2 = (void (*)(void (*)(void *), void *, int, uintptr_t))crosscall2;
    go_dispatch = (void (*)(void *))dispatch;
}

PyObject *pythago_py_fastcall(PyObject *self, PyObject *const *args, Py_ssize_t nargs,
                              PyObject *kwnames)
{
    if (atomic_load(&in_forked_child)) {
        PyErr_SetString(PyExc_RuntimeError,
                        "Go functions are not available in a forked child process");
        return NULL;
    }
    struct pythago_call c = {
        .args = args,
        .nargs = nargs,
        .kwnames = kwnames,
        .nkw = kwnames != NULL ? PyTuple_GET_SIZE(kwnames) : 0,
        .index = PyLong_AsSsize_t(self),
    };
    if (c.index < 0) {
        if (!PyErr_Occurred()) {
            PyErr_SetString(PyExc_SystemError, "invalid Go function index");
        }
        return NULL;
    }
    Py_ssize_t n = nargs + c.nkw;
    struct pythago_value small[PYTHAGO_STACK_VALUES];
    struct pythago_value *values = small;
    if (n + c.nkw > PYTHAGO_STACK_VALUES) {
        values = PyMem_Malloc((size_t)(n + c.nkw) * sizeof *values);
        if (values == NULL) {
            return PyErr_NoMemory();
        }
    }
    c.decoded = values;
    c.names = values + n;
    for (Py_ssize_t i = 0; i < n; i++) {
        decode(args[i], &c.decoded[i]);
    }
    for (Py_ssize_t i = 0; i < c.nkw; i++) {
        decode(PyTuple_GET_ITEM(kwnames, i), &c.names[i]);
    }
    depth++;
#ifdef _WIN32
    /* go_dispatch is a syscall.NewCallback, which Go accepts from any thread. */
    go_dispatch(&c);
#else
    go_crosscall2(go_dispatch, &c, (int)sizeof c, 0);
#endif
    depth--;
    if (values != small) {
        PyMem_Free(values);
    }
    if (c.result != NULL && c.borrowed) {
        Py_INCREF(c.result);
    }
    return c.result;
}

/* Containers built in one call: Go (py/convert.go build) describes the items as tagged
 * values, with small str and bytes payloads copied into one arena, so a tuple, list or
 * dict of scalars costs one Go->C call instead of one or more per item. */
enum { BUILD_TUPLE, BUILD_LIST, BUILD_DICT };

static PyObject *value_new(const struct pythago_value *v, const char *arena)
{
    switch (v->tag) {
    case PV_OBJECT:
        return (PyObject *)(uintptr_t)v->a;
    case PV_NONE:
        return Py_NewRef(Py_None);
    case PV_TRUE:
        return Py_NewRef(Py_True);
    case PV_FALSE:
        return Py_NewRef(Py_False);
    case PV_INT:
        return PyLong_FromLongLong((long long)v->a);
    case PV_UINT:
        return PyLong_FromUnsignedLongLong((unsigned long long)v->a);
    case PV_FLOAT: {
        double d;
        memcpy(&d, &v->a, sizeof d);
        return PyFloat_FromDouble(d);
    }
    case PV_STR:
    case PV_BYTES: {
        /* An offset into the arena, or, without one, the payload's address. */
        const char *p = arena != NULL ? arena + v->a : (const char *)(uintptr_t)v->a;
        if (v->b == 0) {
            p = "";
        }
        return v->tag == PV_STR ? PyUnicode_FromStringAndSize(p, (Py_ssize_t)v->b)
                                : PyBytes_FromStringAndSize(p, (Py_ssize_t)v->b);
    }
    case PV_BORROWED:
        return Py_NewRef((PyObject *)(uintptr_t)v->a);
    }
    PyErr_SetString(PyExc_SystemError, "pythago: invalid value tag");
    return NULL;
}

/* Release the PV_OBJECT references of items[from:n], which a failed build never used. */
static void values_release(const struct pythago_value *items, Py_ssize_t from, Py_ssize_t n)
{
    for (Py_ssize_t i = from; i < n; i++) {
        if (items[i].tag == PV_OBJECT) {
            Py_DECREF((PyObject *)(uintptr_t)items[i].a);
        }
    }
}

/* pythago_py_build returns a new tuple or list of the n items, or a dict of the n/2
 * (key, value) pairs in items; NULL with an exception on failure. It consumes every
 * PV_OBJECT reference in items, also on failure. Attached. */
PyObject *pythago_py_build(int kind, Py_ssize_t n, const struct pythago_value *items,
                           const char *arena)
{
    PyObject *c;
    Py_ssize_t i = 0;
    switch (kind) {
    case BUILD_TUPLE:
        c = PyTuple_New(n);
        break;
    case BUILD_LIST:
        c = PyList_New(n);
        break;
    case BUILD_DICT:
        c = n % 2 == 0 ? PyDict_New() : NULL;
        if (n % 2 != 0) {
            PyErr_SetString(PyExc_SystemError, "pythago: odd number of dict items");
        }
        break;
    default:
        c = NULL;
        PyErr_SetString(PyExc_SystemError, "pythago: invalid container kind");
    }
    if (c == NULL) {
        values_release(items, 0, n);
        return NULL;
    }
    if (kind == BUILD_DICT) {
        for (; i < n; i += 2) {
            PyObject *k = value_new(&items[i], arena);
            if (k == NULL) {
                Py_DECREF(c);
                values_release(items, i + 1, n);
                return NULL;
            }
            PyObject *v = value_new(&items[i + 1], arena);
            int rc = v != NULL ? PyDict_SetItem(c, k, v) : -1;
            Py_DECREF(k);
            Py_XDECREF(v);
            if (rc != 0) {
                Py_DECREF(c);
                values_release(items, i + 2, n);
                return NULL;
            }
        }
        return c;
    }
    for (; i < n; i++) {
        PyObject *o = value_new(&items[i], arena);
        if (o == NULL) { /* PV_OBJECT never fails, so items[i] holds no reference */
            Py_DECREF(c);
            values_release(items, i + 1, n);
            return NULL;
        }
        if (kind == BUILD_LIST) {
            PyList_SET_ITEM(c, i, o);
        } else {
            PyTuple_SET_ITEM(c, i, o);
        }
    }
    return c;
}

/* pythago_py_call calls callable with the n arguments described by items (as for
 * pythago_py_build) and kwargs (a dict, or NULL): one Go->C call for the arguments, the
 * call and their release. It consumes every PV_OBJECT reference in items, also on
 * failure, and returns a new reference, or NULL with an exception. Attached. */
PyObject *pythago_py_call(PyObject *callable, Py_ssize_t n, const struct pythago_value *items,
                          const char *arena, PyObject *kwargs)
{
    PyObject *small[8];
    PyObject **argv = small;
    if (n > (Py_ssize_t)(sizeof small / sizeof small[0])) {
        argv = PyMem_Malloc((size_t)n * sizeof *argv);
        if (argv == NULL) {
            values_release(items, 0, n);
            return PyErr_NoMemory();
        }
    }
    Py_ssize_t i = 0;
    PyObject *r = NULL;
    for (; i < n; i++) {
        if ((argv[i] = value_new(&items[i], arena)) == NULL) {
            values_release(items, i + 1, n);
            goto done;
        }
    }
    r = PyObject_VectorcallDict(callable, argv, (size_t)n, kwargs);
done:
    for (Py_ssize_t j = 0; j < i; j++) {
        Py_DECREF(argv[j]);
    }
    if (argv != small) {
        PyMem_Free(argv);
    }
    return r;
}

/* ---- Experimental: a combiner that stays in C (.agents/attic/spike/sinline, py/spike_*.go).
 *
 * Go request goroutines submit jobs, each a call's arguments as tagged values (with a
 * NULL arena, str and bytes payloads are addresses of Go memory the submitter keeps
 * alive). One Python thread runs pythago_comb_run: it takes every submitted job, builds
 * all their arguments, calls fn(*arguments) once per batch, and hands the result to a
 * completer goroutine waiting in pythago_comb_take. It never returns to Go, so it never
 * waits for a Go scheduler slot while it holds the GIL. */
struct pythago_job { /* py/spike_combiner.go cjob */
    struct pythago_job *next;
    const struct pythago_value *items;
    Py_ssize_t nitems;
};

struct pythago_batch {
    struct pythago_batch *next;
    struct pythago_job *jobs; /* in submission order */
    PyObject *result;         /* a new reference, or NULL */
    const char *data;         /* result's bytes */
    Py_ssize_t len;
};

#define PYTHAGO_COMB_OUTS 16

struct comb_out { /* one completer's queue: finished batches, newest first */
    _Atomic(struct pythago_batch *) box;
    atomic_uint seq, sleep; /* the completer's futex */
    char pad[48];           /* a cache line each */
};

struct pythago_comb {
    _Atomic(struct pythago_job *) inbox;     /* submitted, newest first */
    atomic_uint inseq, insleep;              /* the combiner's futex */
    struct comb_out out[PYTHAGO_COMB_OUTS];  /* batches go round-robin to nout completers */
    unsigned nout, next;
    _Atomic(struct pythago_batch *) recycle; /* consumed by the completer */
    PyObject *fn;
    /* the combiner's counters: batches, jobs, sleeps, then ns asleep, building the
     * arguments, in fn, and handing off (pythago_comb_stats) */
    uint64_t stats[7];
};

static uint64_t comb_now(void)
{
#ifdef _WIN32
    static LARGE_INTEGER freq;
    LARGE_INTEGER now;
    if (freq.QuadPart == 0) {
        QueryPerformanceFrequency(&freq);
    }
    QueryPerformanceCounter(&now);
    return (uint64_t)(now.QuadPart / freq.QuadPart * 1000000000u +
                      now.QuadPart % freq.QuadPart * 1000000000u / freq.QuadPart);
#else
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000000u + (uint64_t)ts.tv_nsec;
#endif
}

void pythago_comb_stats(struct pythago_comb *c, uint64_t *out)
{
    memcpy(out, c->stats, sizeof c->stats);
}

static void comb_wake(atomic_uint *seq, atomic_uint *sleeping)
{
    if (atomic_exchange(sleeping, 0)) {
        atomic_fetch_add(seq, 1);
        sys_wake(seq, 0);
    }
}

void *pythago_comb_new(PyObject *fn, unsigned nout)
{
    struct pythago_comb *c = PyMem_RawCalloc(1, sizeof *c);
    if (c != NULL) {
        c->fn = Py_NewRef(fn);
        c->nout = nout < 1 ? 1 : nout > PYTHAGO_COMB_OUTS ? PYTHAGO_COMB_OUTS : nout;
    }
    return c;
}

void pythago_comb_push(struct pythago_comb *c, struct pythago_job *j)
{
    struct pythago_job *head = atomic_load(&c->inbox);
    do {
        j->next = head;
    } while (!atomic_compare_exchange_weak(&c->inbox, &head, j));
    comb_wake(&c->inseq, &c->insleep);
}

/* pythago_comb_take waits for finished batches in completer k's queue and returns them,
 * oldest first. No GIL. */
struct pythago_batch *pythago_comb_take(struct pythago_comb *c, unsigned k)
{
    struct comb_out *o = &c->out[k];
    for (;;) {
        unsigned seq = atomic_load(&o->seq);
        atomic_store(&o->sleep, 1);
        struct pythago_batch *b = atomic_exchange(&o->box, NULL);
        if (b != NULL) {
            atomic_store(&o->sleep, 0);
            struct pythago_batch *rev = NULL;
            while (b != NULL) {
                struct pythago_batch *n = b->next;
                b->next = rev;
                rev = b;
                b = n;
            }
            return rev;
        }
        futex_wait(&o->seq, seq);
    }
}

/* pythago_comb_done hands consumed batches back; the combiner releases their results. */
void pythago_comb_done(struct pythago_comb *c, struct pythago_batch *b)
{
    while (b != NULL) {
        struct pythago_batch *n = b->next;
        struct pythago_batch *head = atomic_load(&c->recycle);
        do {
            b->next = head;
        } while (!atomic_compare_exchange_weak(&c->recycle, &head, b));
        b = n;
    }
}

/* pythago_comb_run is the combiner loop; it does not return. Attached. */
void pythago_comb_run(struct pythago_comb *c)
{
    PyObject *small[64];
    for (;;) {
        for (struct pythago_batch *b = atomic_exchange(&c->recycle, NULL); b != NULL;) {
            struct pythago_batch *n = b->next;
            Py_XDECREF(b->result);
            PyMem_RawFree(b);
            b = n;
        }
        unsigned seq = atomic_load(&c->inseq);
        atomic_store(&c->insleep, 1);
        struct pythago_job *j = atomic_exchange(&c->inbox, NULL);
        if (j == NULL) {
            uint64_t t0 = comb_now();
            Py_BEGIN_ALLOW_THREADS
            futex_wait(&c->inseq, seq);
            Py_END_ALLOW_THREADS
            c->stats[2]++;
            c->stats[3] += comb_now() - t0;
            continue;
        }
        atomic_store(&c->insleep, 0);
        uint64_t t1 = comb_now();
        struct pythago_job *jobs = NULL; /* reverse into submission order */
        Py_ssize_t n = 0;
        while (j != NULL) {
            struct pythago_job *next = j->next;
            j->next = jobs;
            jobs = j;
            n += j->nitems;
            j = next;
        }
        PyObject **argv = n <= 64 ? small : PyMem_Malloc((size_t)n * sizeof *argv);
        Py_ssize_t k = 0;
        for (struct pythago_job *q = jobs; q != NULL; q = q->next) {
            for (Py_ssize_t i = 0; i < q->nitems; i++) {
                PyObject *o = value_new(&q->items[i], NULL);
                if (o == NULL) {
                    PyErr_Clear();
                    o = Py_NewRef(Py_None);
                }
                argv[k++] = o;
            }
        }
        struct pythago_batch *b = PyMem_RawCalloc(1, sizeof *b);
        b->jobs = jobs;
        uint64_t t2 = comb_now();
        b->result = PyObject_Vectorcall(c->fn, argv, (size_t)n, NULL);
        uint64_t t3 = comb_now();
        c->stats[0]++;
        c->stats[1] += (uint64_t)(n / 8);
        c->stats[4] += t2 - t1;
        c->stats[5] += t3 - t2;
        for (Py_ssize_t i = 0; i < n; i++) {
            Py_DECREF(argv[i]);
        }
        if (argv != small) {
            PyMem_Free(argv);
        }
        char *data;
        if (b->result == NULL || PyBytes_AsStringAndSize(b->result, &data, &b->len) < 0) {
            PyErr_Print();
            Py_CLEAR(b->result);
            b->data = NULL;
            b->len = 0;
        } else {
            b->data = data;
        }
        struct comb_out *o = &c->out[c->next++ % c->nout];
        struct pythago_batch *head = atomic_load(&o->box);
        do {
            b->next = head;
        } while (!atomic_compare_exchange_weak(&o->box, &head, b));
        comb_wake(&o->seq, &o->sleep);
        c->stats[6] += comb_now() - t3;
    }
}

/* Object kinds for conversion, so Go never reads object layouts. */
int pythago_py_kind(PyObject *o)
{
    if (o == Py_None) return 1;
    if (PyBool_Check(o)) return 2;
    if (PyLong_Check(o)) return 3;
    if (PyFloat_Check(o)) return 4;
    if (PyUnicode_Check(o)) return 5;
    if (PyBytes_Check(o)) return 6;
    if (PyByteArray_Check(o)) return 7;
    if (PyList_Check(o)) return 8;
    if (PyTuple_Check(o)) return 9;
    if (PyDict_Check(o)) return 10;
    return 0;
}

int pythago_py_gil_disabled(void)
{
#ifdef Py_GIL_DISABLED
    return 1;
#else
    return 0;
#endif
}

void pythago_py_stats(pythago_long *out)
{
    out[0] = atomic_load(&owned_created);
    out[1] = atomic_load(&owned_deleted);
    out[2] = atomic_load(&live);
    out[3] = atomic_load(&started_count);
}

/*
 * Signals belong to Go: CPython runs with install_signal_handlers = 0, and signal.signal()
 * returns None for handlers it did not install. Code that saves and later restores a
 * handler (multiprocessing.forkserver) then calls signal.signal(sig, None), which CPython
 * rejects. Package py gives None the meaning "restore the handler from before Python",
 * using the handlers saved here at startup; in a forked child, where Go cannot run, it
 * leaves SIG_DFL instead.
 */
#ifndef _WIN32
static struct sigaction saved_handlers[NSIG];
#endif

static PyObject *restore_signal(PyObject *self, PyObject *arg)
{
    (void)self;
    int sig = PyLong_AsInt(arg);
    if (sig == -1 && PyErr_Occurred()) {
        return NULL;
    }
#ifdef _WIN32
    /* Go's handlers are not signal handlers on Windows (console control events), and
     * there is no fork: nothing to restore. */
    Py_RETURN_NONE;
#else
    if (sig <= 0 || sig >= NSIG) {
        PyErr_SetString(PyExc_ValueError, "signal number out of range");
        return NULL;
    }
    if (!atomic_load(&in_forked_child) && sigaction(sig, &saved_handlers[sig], NULL) != 0) {
        return PyErr_SetFromErrno(PyExc_OSError);
    }
    Py_RETURN_NONE;
#endif
}

static PyMethodDef pythago_methods[] = {
    {"restore_signal", restore_signal, METH_O,
     "Reinstall the handler that signal had before Python (Go's), or SIG_DFL in a forked child."},
    {NULL, NULL, 0, NULL},
};

static struct PyModuleDef pythago_module = {
    PyModuleDef_HEAD_INIT, "_pythago", "Support for package py.", -1, pythago_methods,
};

/* pythago_py_install_module saves every signal's handler and adds the _pythago module to
 * sys.modules; it runs on the main thread with its thread state attached. */
int pythago_py_install_module(void)
{
#ifndef _WIN32
    for (int sig = 1; sig < NSIG; sig++) {
        sigaction(sig, NULL, &saved_handlers[sig]);
    }
#endif
    PyObject *m = PyModule_Create(&pythago_module);
    if (m == NULL) {
        return -1;
    }
    int rc = PyDict_SetItemString(PyImport_GetModuleDict(), "_pythago", m);
    Py_DECREF(m);
    return rc;
}

/*
 * _pythago_fs: the file systems package py mounts under /pythago-fs, served from an
 * immutable index that Go builds at initialization: entries sorted by full virtual path,
 * each a directory or a file whose bytes stay alive and in place for the life of the
 * process (the embedded stdlib zip's stored entries, or Go-held copies). Everything here
 * is C, so imports keep working in forked children, where Go cannot run.
 */
typedef struct pythago_fs_entry {
    const char *path;
    size_t path_len;
    const char *data; /* NULL for a directory */
    size_t size;
} pythago_fs_entry;

static const pythago_fs_entry *fs_entries;
static size_t fs_count;

static int fs_cmp(const char *a, size_t alen, const char *b, size_t blen)
{
    int c = memcmp(a, b, alen < blen ? alen : blen);
    return c != 0 ? c : (alen > blen) - (alen < blen);
}

/* Index of the first entry not less than (p, n). */
static size_t fs_lower(const char *p, size_t n)
{
    size_t lo = 0, hi = fs_count;
    while (lo < hi) {
        size_t mid = lo + (hi - lo) / 2;
        if (fs_cmp(fs_entries[mid].path, fs_entries[mid].path_len, p, n) < 0) {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    return lo;
}

static const pythago_fs_entry *fs_find(PyObject *arg)
{
    Py_ssize_t n;
    const char *p = PyUnicode_AsUTF8AndSize(arg, &n);
    if (p == NULL) {
        return NULL;
    }
    size_t i = fs_lower(p, (size_t)n);
    if (i < fs_count && fs_cmp(fs_entries[i].path, fs_entries[i].path_len, p, (size_t)n) == 0) {
        return &fs_entries[i];
    }
    return NULL;
}

static PyObject *fs_stat(PyObject *self, PyObject *arg)
{
    (void)self;
    const pythago_fs_entry *e = fs_find(arg);
    if (e == NULL && PyErr_Occurred()) {
        return NULL;
    }
    return PyLong_FromLong(e == NULL ? 0 : e->data == NULL ? 2 : 1);
}

static PyObject *fs_read(PyObject *self, PyObject *arg)
{
    (void)self;
    const pythago_fs_entry *e = fs_find(arg);
    if (e == NULL && PyErr_Occurred()) {
        return NULL;
    }
    if (e == NULL || e->data == NULL) {
        Py_RETURN_NONE;
    }
    return PyBytes_FromStringAndSize(e->data, (Py_ssize_t)e->size);
}

static PyObject *fs_listdir(PyObject *self, PyObject *arg)
{
    (void)self;
    Py_ssize_t n;
    const char *p = PyUnicode_AsUTF8AndSize(arg, &n);
    if (p == NULL) {
        return NULL;
    }
    PyObject *list = PyList_New(0);
    if (list == NULL) {
        return NULL;
    }
    /* Children of "p/" sort right after "p/" itself; stop at the first non-match. */
    char *prefix = PyMem_RawMalloc((size_t)n + 2);
    if (prefix == NULL) {
        Py_DECREF(list);
        return PyErr_NoMemory();
    }
    memcpy(prefix, p, (size_t)n);
    prefix[n] = '/';
    size_t plen = (size_t)n + 1;
    for (size_t i = fs_lower(prefix, plen); i < fs_count; i++) {
        const pythago_fs_entry *e = &fs_entries[i];
        if (e->path_len <= plen || memcmp(e->path, prefix, plen) != 0) {
            break;
        }
        const char *rest = e->path + plen;
        size_t rlen = e->path_len - plen;
        if (memchr(rest, '/', rlen) != NULL) {
            continue; /* deeper than one level */
        }
        PyObject *name = PyUnicode_FromStringAndSize(rest, (Py_ssize_t)rlen);
        if (name == NULL || PyList_Append(list, name) < 0) {
            Py_XDECREF(name);
            Py_DECREF(list);
            PyMem_RawFree(prefix);
            return NULL;
        }
        Py_DECREF(name);
    }
    PyMem_RawFree(prefix);
    return list;
}

static PyMethodDef fs_methods[] = {
    {"stat", fs_stat, METH_O, "0: missing, 1: file, 2: directory."},
    {"read", fs_read, METH_O, "The file's bytes, or None."},
    {"listdir", fs_listdir, METH_O, "Names in a directory (empty if missing)."},
    {NULL, NULL, 0, NULL},
};

static struct PyModuleDef fs_module = {
    PyModuleDef_HEAD_INIT, "_pythago_fs", "Go file systems mounted under /pythago-fs.", -1, fs_methods,
};

/* pythago_fs_install installs the index (owned by Go, never freed) and the _pythago_fs
 * module; it runs in the core phase of initialization, attached. */
int pythago_fs_install(const pythago_fs_entry *entries, size_t count)
{
    fs_entries = entries;
    fs_count = count;
    PyObject *m = PyModule_Create(&fs_module);
    if (m == NULL) {
        return -1;
    }
    int rc = PyDict_SetItemString(PyImport_GetModuleDict(), "_pythago_fs", m);
    Py_DECREF(m);
    return rc;
}

/*
 * Go slices exported to Python through the buffer protocol (py.NewBuffer). A GoBuffer
 * holds a pointer into Go memory that Go keeps pinned under a token. Its deallocator does
 * not call Go: it pushes the token onto a lock-free list that Go drains on its next entry
 * into Python, and unpins then. In a forked child nothing drains the list, harmlessly.
 */
typedef struct gobuf_release {
    struct gobuf_release *next;
    unsigned long long token;
} gobuf_release;

static _Atomic(gobuf_release *) gobuf_released; /* pushed by deallocators */
static gobuf_release *gobuf_draining;           /* Go's single consumer takes the list here */
static _Atomic(pythago_long) gobuf_released_count;
static _Atomic(PyTypeObject *) gobuf_type;

typedef struct {
    PyObject_HEAD
    void *buf;
    Py_ssize_t len, itemsize;
    int readonly, ndim;
    unsigned long long token;
    char format[4];
    Py_ssize_t *dims; /* ndim shapes, then ndim C-order strides */
} gobuf;

static int gobuf_getbuffer(PyObject *self, Py_buffer *view, int flags)
{
    gobuf *b = (gobuf *)self;
    if ((flags & PyBUF_WRITABLE) && b->readonly) {
        PyErr_SetString(PyExc_BufferError, "the Go buffer is read-only");
        view->obj = NULL;
        return -1;
    }
    view->buf = b->buf;
    view->obj = Py_NewRef(self);
    view->len = b->len;
    view->itemsize = b->itemsize;
    view->readonly = b->readonly;
    view->format = (flags & PyBUF_FORMAT) ? b->format : NULL;
    if (flags & PyBUF_ND) {
        view->ndim = b->ndim;
        view->shape = b->dims;
    } else { /* a plain run of len bytes */
        view->ndim = 1;
        view->shape = NULL;
    }
    view->strides = (flags & PyBUF_STRIDES) == PyBUF_STRIDES ? b->dims + b->ndim : NULL;
    view->suboffsets = NULL;
    view->internal = NULL;
    return 0;
}

static void gobuf_dealloc(PyObject *self)
{
    gobuf *b = (gobuf *)self;
    gobuf_release *r = PyMem_RawMalloc(sizeof *r); /* if NULL, the Go memory stays pinned */
    if (r != NULL) {
        r->token = b->token;
        r->next = atomic_load(&gobuf_released);
        while (!atomic_compare_exchange_weak(&gobuf_released, &r->next, r)) {
        }
        atomic_fetch_add(&gobuf_released_count, 1);
    }
    PyMem_RawFree(b->dims);
    PyTypeObject *tp = Py_TYPE(self);
    ((freefunc)PyType_GetSlot(tp, Py_tp_free))(self);
    Py_DECREF(tp);
}

static PyType_Slot gobuf_slots[] = {
    {Py_bf_getbuffer, gobuf_getbuffer},
    {Py_tp_dealloc, gobuf_dealloc},
    {Py_tp_doc, "Go memory exported through the buffer protocol (py.NewBuffer)."},
    {0, NULL},
};

static PyType_Spec gobuf_spec = {
    "pythago.GoBuffer", sizeof(gobuf), 0, Py_TPFLAGS_DEFAULT | Py_TPFLAGS_DISALLOW_INSTANTIATION, gobuf_slots,
};

/* pythago_gobuf_new returns a GoBuffer over buf (len bytes of C-contiguous items), with
 * format (at most 3 characters) and ndim >= 1 dimensions; NULL with an exception on
 * failure. Attached. */
PyObject *pythago_gobuf_new(void *buf, Py_ssize_t len, Py_ssize_t itemsize, int readonly,
                            const char *format, int ndim, const Py_ssize_t *shape,
                            unsigned long long token)
{
    PyTypeObject *tp = atomic_load(&gobuf_type);
    if (tp == NULL) {
        tp = (PyTypeObject *)PyType_FromSpec(&gobuf_spec);
        if (tp == NULL) {
            return NULL;
        }
        PyTypeObject *prev = NULL;
        if (!atomic_compare_exchange_strong(&gobuf_type, &prev, tp)) {
            Py_DECREF(tp); /* another thread created it first */
            tp = prev;
        }
    }
    if (ndim < 1 || ndim > PyBUF_MAX_NDIM || strlen(format) >= sizeof(((gobuf *)0)->format)) {
        PyErr_SetString(PyExc_ValueError, "invalid Go buffer layout");
        return NULL;
    }
    Py_ssize_t *dims = PyMem_RawMalloc(2 * (size_t)ndim * sizeof(Py_ssize_t));
    if (dims == NULL) {
        return PyErr_NoMemory();
    }
    Py_ssize_t stride = itemsize;
    for (int i = ndim - 1; i >= 0; i--) {
        dims[i] = shape[i];
        dims[ndim + i] = stride;
        stride *= shape[i];
    }
    gobuf *b = PyObject_New(gobuf, tp);
    if (b == NULL) {
        PyMem_RawFree(dims);
        return NULL;
    }
    b->buf = buf;
    b->len = len;
    b->itemsize = itemsize;
    b->readonly = readonly;
    b->ndim = ndim;
    b->token = token;
    strcpy(b->format, format);
    b->dims = dims;
    return (PyObject *)b;
}

/* pythago_gobuf_released returns the address of the count of tokens waiting, which Go
 * reads before calling pythago_gobuf_drain. */
_Atomic(pythago_long) *pythago_gobuf_released(void) { return &gobuf_released_count; }

/* pythago_gobuf_drain moves up to max released tokens to out and returns how many. Go
 * calls it from one goroutine at a time (the single consumer), so taking the whole list
 * with an exchange needs no ABA care. */
size_t pythago_gobuf_drain(unsigned long long *out, size_t max)
{
    size_t n = 0;
    while (n < max) {
        if (gobuf_draining == NULL) {
            gobuf_draining = atomic_exchange(&gobuf_released, NULL);
            if (gobuf_draining == NULL) {
                break;
            }
        }
        gobuf_release *r = gobuf_draining;
        gobuf_draining = r->next;
        out[n++] = r->token;
        PyMem_RawFree(r);
    }
    atomic_fetch_sub(&gobuf_released_count, (pythago_long)n);
    return n;
}

/* ---- The fusion: Go-owned bytes objects (py.Config.FusedBytes; internal/fusion, whose
 * package comment has the design, and .agents/docs/arena-design.md sections 13 to 15).
 *
 * Go reads a body into a slot of a chunk of Go memory and pythago_bytes_adopt makes the
 * slot a bytes object in place. Python frees it through PyObject_Free (or moves it with
 * PyObject_Realloc in _PyBytes_Resize): the OBJ-domain hook below recognizes a slot,
 * pushes it on its chunk's free stack and decrements the chunk's live count; any other
 * pointer goes to the allocator the hook wraps.
 *
 * Invariant: the hook never writes a slot. A freed object keeps the bytes before ob_type
 * as Python left them (a dead header that a stale lock-free reader refuses to incref
 * without writing), as mimalloc keeps them for a block reused in the same size class. The
 * free stack's links live in a Go array (cell->links), never in the slot. C only pushes, a
 * lock-free CAS loop that takes no lock Go may hold; Go pops under its own mutex. The
 * stamp (an atomic max with the epoch) comes before the push, the live decrement (release)
 * after it and last: once Go sees live 0 (acquire), the hook touches nothing of the chunk
 * any more, and Go may give it another class or back to the Go heap once it is also past
 * a collection's stop-the-world (free-threaded). */
struct pythago_fusion_cell {
    _Atomic(uint64_t) head;   /* free stack: tag << 32 | (slot index + 1); 0 index: empty */
    _Atomic(int64_t) live;    /* slots taken by Go or holding a live object */
    _Atomic(uint64_t) stamp;  /* epoch of the last free by Python */
    _Atomic(uintptr_t) base;  /* the chunk's address; 0: none */
    _Atomic(uintptr_t) links; /* _Atomic(uint32_t)[slots]: the free stack's links */
    _Atomic(uint32_t) dshift; /* slot size = 2^dshift * odd: index = (offset >> dshift) * dinv */
    _Atomic(uint32_t) dinv;   /* the odd part's inverse mod 2^32 */
};

/* struct pythago_fusion_config: Go memory, pinned for the life of the process. */
struct pythago_fusion_config {
    uintptr_t cells;      /* struct pythago_fusion_cell[ncells] */
    uintptr_t ncells;
    uintptr_t chunk_size; /* FUSION_CHUNK */
    uintptr_t l1;         /* _Atomic(uintptr_t)[1 << FUSION_L1_BITS]: l2 arrays or 0 */
    uintptr_t epoch;      /* _Atomic(uint64_t): bumped at each collection's start */
};

/* The lookup: 1 MiB address windows, a two-level radix table over 48-bit addresses. An l2
 * entry holds the cell indices (+1) of the at most two chunks that overlap its window. */
#define FUSION_CHUNK ((uintptr_t)1 << 20)
#define FUSION_WINDOW_SHIFT 20
#define FUSION_L2_BITS 14
#define FUSION_L1_BITS (48 - FUSION_WINDOW_SHIFT - FUSION_L2_BITS)

static PyMemAllocatorEx fusion_orig;
static struct pythago_fusion_cell *fusion_cells;
static _Atomic(uintptr_t) *fusion_l1;
static _Atomic(uint64_t) *fusion_epoch;
/* The lowest and highest + 1 registered chunk addresses, which Go writes (under its mutex)
 * through pythago_fusion_bounds: in static storage, so that the check every free pays is
 * two independent loads. Empty (no chunk) until Go registers one. */
static _Atomic(uintptr_t) fusion_bounds[2] = {UINTPTR_MAX, 0};

_Atomic(uintptr_t) *pythago_fusion_bounds(void) { return fusion_bounds; }

/* fusion_find returns the cell whose chunk holds a, with a's offset in it, or NULL. The
 * bounds reject almost every pointer at one compare (Python's own object memory is never
 * Go heap). Relaxed loads suffice: a live fused object's chunk was registered before Go
 * handed out its slot, and its pointer reached this thread through Python's own
 * synchronization; for any other pointer no chunk can hold it, whatever is read. */
static inline struct pythago_fusion_cell *fusion_find(uintptr_t a, uintptr_t *off)
{
    if (a < atomic_load_explicit(&fusion_bounds[0], memory_order_relaxed) ||
        a >= atomic_load_explicit(&fusion_bounds[1], memory_order_relaxed)) {
        return NULL;
    }
    uintptr_t w = a >> FUSION_WINDOW_SHIFT;
    _Atomic(uint64_t) *l2 = (_Atomic(uint64_t) *)atomic_load_explicit(
        &fusion_l1[w >> FUSION_L2_BITS], memory_order_relaxed);
    if (l2 == NULL) {
        return NULL;
    }
    uint64_t e = atomic_load_explicit(&l2[w & (((uintptr_t)1 << FUSION_L2_BITS) - 1)],
                                      memory_order_relaxed);
    for (; e != 0; e >>= 32) {
        uint32_t id = (uint32_t)e;
        if (id == 0) {
            continue;
        }
        struct pythago_fusion_cell *c = &fusion_cells[id - 1];
        uintptr_t o = a - atomic_load_explicit(&c->base, memory_order_relaxed);
        if (o < FUSION_CHUNK) {
            *off = o;
            return c;
        }
    }
    return NULL;
}

static void fusion_release(struct pythago_fusion_cell *c, uintptr_t off)
{
    uint32_t idx = (uint32_t)(off >> atomic_load_explicit(&c->dshift, memory_order_relaxed)) *
                   atomic_load_explicit(&c->dinv, memory_order_relaxed);
    uint64_t e = atomic_load(fusion_epoch);
    uint64_t s = atomic_load(&c->stamp);
    while (s < e && !atomic_compare_exchange_weak(&c->stamp, &s, e)) {
    }
    _Atomic(uint32_t) *link =
        (_Atomic(uint32_t) *)atomic_load_explicit(&c->links, memory_order_relaxed) + idx;
    uint64_t old = atomic_load_explicit(&c->head, memory_order_relaxed), nw;
    do {
        atomic_store_explicit(link, (uint32_t)old, memory_order_relaxed);
        nw = (((old >> 32) + 1) << 32) | (uint64_t)(idx + 1);
    } while (!atomic_compare_exchange_weak_explicit(&c->head, &old, nw, memory_order_release,
                                                    memory_order_relaxed));
    atomic_fetch_sub_explicit(&c->live, 1, memory_order_release);
}

static void fusion_free(void *ctx, void *p)
{
    (void)ctx;
    uintptr_t off;
    struct pythago_fusion_cell *c = fusion_find((uintptr_t)p, &off);
    if (c != NULL) {
        fusion_release(c, off);
        return;
    }
    fusion_orig.free(fusion_orig.ctx, p);
}

static void *fusion_realloc(void *ctx, void *p, size_t n)
{
    (void)ctx;
    uintptr_t off;
    struct pythago_fusion_cell *c = p != NULL ? fusion_find((uintptr_t)p, &off) : NULL;
    if (c != NULL) {
        /* Only bytes live in a slot: _PyBytes_Resize of a uniquely referenced object. Move
         * it out into the wrapped allocator; the old slot keeps its header (as mimalloc's
         * realloc keeps a moved block's) and goes back to its free stack. */
        size_t old = (size_t)(offsetof(PyBytesObject, ob_sval) + Py_SIZE((PyObject *)p) + 1);
        void *q = fusion_orig.malloc(fusion_orig.ctx, n);
        if (q != NULL) {
            memcpy(q, p, old < n ? old : n);
            fusion_release(c, off);
        }
        return q;
    }
    return fusion_orig.realloc(fusion_orig.ctx, p, n);
}

/* pythago_fusion_supported reports whether the glue was compiled against a CPython the
 * fusion is verified on (arena-design.md section 13's per-release checks): 3.13 with the
 * GIL, 3.14 free-threaded. */
int pythago_fusion_supported(void)
{
#if PY_MAJOR_VERSION == 3 && PY_MINOR_VERSION == 13 && !defined(Py_GIL_DISABLED)
    return 1;
#elif PY_MAJOR_VERSION == 3 && PY_MINOR_VERSION == 14 && defined(Py_GIL_DISABLED)
    return 1;
#else
    return 0;
#endif
}

/* pythago_fusion_install wraps the OBJ-domain allocator. Attached, between the core and
 * the main phase of initialization: before tracemalloc can start (main phase), which saves
 * the allocator it wraps and restores it when stopped, so it saves and restores ours. */
int pythago_fusion_install(const struct pythago_fusion_config *cfg)
{
    if (cfg->chunk_size != FUSION_CHUNK) {
        return -1;
    }
    PyMemAllocatorEx a;
    PyMem_GetAllocator(PYMEM_DOMAIN_OBJ, &fusion_orig);
    fusion_cells = (struct pythago_fusion_cell *)cfg->cells;
    fusion_l1 = (_Atomic(uintptr_t) *)cfg->l1;
    fusion_epoch = (_Atomic(uint64_t) *)cfg->epoch;
    a = fusion_orig;
    a.free = fusion_free;
    a.realloc = fusion_realloc;
    PyMem_SetAllocator(PYMEM_DOMAIN_OBJ, &a);
    return 0;
}

/* pythago_fusion_hooked reports whether the OBJ-domain allocator's free is the fusion's
 * hook now (tests: nothing removed it). */
int pythago_fusion_hooked(void)
{
    PyMemAllocatorEx a;
    PyMem_GetAllocator(PYMEM_DOMAIN_OBJ, &a);
    return a.free == fusion_free && a.realloc == fusion_realloc;
}

/* pythago_fusion_find returns the cell whose chunk the hook finds p in, or -1 (tests). */
Py_ssize_t pythago_fusion_find(void *p)
{
    uintptr_t off;
    if (fusion_cells == NULL) {
        return -1;
    }
    struct pythago_fusion_cell *c = fusion_find((uintptr_t)p, &off);
    return c != NULL ? (Py_ssize_t)(c - fusion_cells) : -1;
}

/* The offset of a bytes object's data, and of ob_type (the end of what a lock-free reader
 * may touch in a freed object). */
Py_ssize_t pythago_bytes_header(void)
{
    return (Py_ssize_t)offsetof(PyBytesObject, ob_sval);
}

Py_ssize_t pythago_bytes_typeoff(void)
{
    return (Py_ssize_t)offsetof(PyObject, ob_type);
}

/* pythago_bytes_adopt makes the slot at p, whose data Go wrote from ob_sval, a bytes
 * object of n >= 2 bytes, as _PyBytes_FromSize does: a new reference. NULL, with no
 * exception set, outside the main interpreter: a subinterpreter's objects are its own,
 * and its collections are not the main interpreter's stop-the-world; the caller copies.
 * Attached. */
PyObject *pythago_bytes_adopt(void *p, Py_ssize_t n)
{
    if (PyInterpreterState_Get() != PyInterpreterState_Main()) {
        return NULL;
    }
    PyBytesObject *op = (PyBytesObject *)PyObject_InitVar((PyVarObject *)p, &PyBytes_Type, n);
#if defined(__clang__) || defined(__GNUC__)
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wdeprecated-declarations"
#endif
    op->ob_shash = -1;
#if defined(__clang__) || defined(__GNUC__)
#pragma GCC diagnostic pop
#endif
    op->ob_sval[n] = '\0';
    return (PyObject *)op;
}
