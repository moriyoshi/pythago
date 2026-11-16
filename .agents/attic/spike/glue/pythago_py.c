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
#include <errno.h>
#include <limits.h>
#include <linux/futex.h>
#include <pthread.h>
#include <sched.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <sys/syscall.h>
#include <unistd.h>

static __thread int depth;            /* >0: this OS thread is inside Python on Go's behalf */
static __thread PyThreadState *owned; /* the thread state py created for this OS thread */
static __thread int slot_held;        /* an outer py.Do on this thread holds an admission slot */
static pthread_key_t owned_key;       /* its destructor frees owned at thread exit */
static pthread_once_t owned_once = PTHREAD_ONCE_INIT;
static atomic_int alive;              /* initialized and not finalizing */
static atomic_int deleting;           /* threads inside owned_drop */
static atomic_long owned_created, owned_deleted;

static void owned_drop(void)
{
    atomic_fetch_add(&deleting, 1);
    if (owned != NULL && atomic_load(&alive)) {
        PyEval_RestoreThread(owned);
        PyThreadState_Clear(owned);
        PyThreadState_DeleteCurrent();
        atomic_fetch_add(&owned_deleted, 1);
    }
    owned = NULL;
    atomic_fetch_sub(&deleting, 1);
}

static void owned_dtor(void *p)
{
    (void)p;
    owned_drop();
}

static void owned_key_init(void) { pthread_key_create(&owned_key, owned_dtor); }

/* pythago_py_started marks the runtime usable, after initialization. */
static void spares_refill(void);
static void spares_stop(void);
static void forked_child(void);

/* glibc keeps pthread_atfork in the static libc_nonshared.a; libc.so.6 exports
 * __register_atfork, which takes the image's __dso_handle (as atexit does). */
extern int __register_atfork(void (*prepare)(void), void (*parent)(void), void (*child)(void),
                             void *dso);
extern char __dso_handle;

void pythago_py_started(void)
{
    pthread_once(&owned_once, owned_key_init);
    __register_atfork(NULL, NULL, forked_child, &__dso_handle);
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
static atomic_long live, limit = 4096, started_count;

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

long pythago_py_set_limit(long n) { return atomic_exchange(&limit, n); }

static size_t bucket_of(pthread_t th)
{
    return (size_t)(((uintptr_t)th >> 4) * 0x9E3779B97F4A7C15ull >> 56) % NBUCKETS;
}

static void futex_wait(atomic_uint *w, unsigned v)
{
    int e = errno;
    syscall(SYS_futex, w, FUTEX_WAIT_PRIVATE, v, NULL, NULL, 0);
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
    syscall(SYS_futex, w, FUTEX_WAKE_PRIVATE, INT_MAX, NULL, NULL, 0);
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

/* Every Go-defined builtin has ml_meth = pythago_py_fastcall (METH_FASTCALL |
 * METH_KEYWORDS), which marks the OS thread as inside Python while the single Go
 * dispatcher runs. */
static PyObject *(*go_dispatch)(PyObject *, PyObject *const *, Py_ssize_t, PyObject *);

void pythago_py_set_dispatch(void *fn)
{
    go_dispatch = (PyObject * (*)(PyObject *, PyObject *const *, Py_ssize_t, PyObject *)) fn;
}

PyObject *pythago_py_fastcall(PyObject *self, PyObject *const *args, Py_ssize_t nargs,
                              PyObject *kwnames)
{
    if (atomic_load(&in_forked_child)) {
        PyErr_SetString(PyExc_RuntimeError,
                        "Go functions are not available in a forked child process");
        return NULL;
    }
    depth++;
    PyObject *r = go_dispatch(self, args, nargs, kwnames);
    depth--;
    return r;
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

void pythago_py_stats(long *out)
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
static struct sigaction saved_handlers[NSIG];

static PyObject *restore_signal(PyObject *self, PyObject *arg)
{
    (void)self;
    int sig = PyLong_AsInt(arg);
    if (sig == -1 && PyErr_Occurred()) {
        return NULL;
    }
    if (sig <= 0 || sig >= NSIG) {
        PyErr_SetString(PyExc_ValueError, "signal number out of range");
        return NULL;
    }
    if (!atomic_load(&in_forked_child) && sigaction(sig, &saved_handlers[sig], NULL) != 0) {
        return PyErr_SetFromErrno(PyExc_OSError);
    }
    Py_RETURN_NONE;
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
    for (int sig = 1; sig < NSIG; sig++) {
        sigaction(sig, NULL, &saved_handlers[sig]);
    }
    PyObject *m = PyModule_Create(&pythago_module);
    if (m == NULL) {
        return -1;
    }
    int rc = PyDict_SetItemString(PyImport_GetModuleDict(), "_pythago", m);
    Py_DECREF(m);
    return rc;
}
