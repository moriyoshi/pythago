/*
 * pythago C glue: wrappers over C API calls whose signatures purego cannot call
 * directly (PyStatus returned by value) or whose structs Go should not lay out
 * (PyConfig). Every pythago_* function is listed in pythago_symtab.
 */
#include <Python.h>

/* glibc keeps atexit in the static libc_nonshared.a; libc.so.6 only exports
 * __cxa_atexit. bao's __dso_handle identifies the translated image. */
extern int __cxa_atexit(void (*fn)(void *), void *arg, void *dso);
extern char __dso_handle;
int atexit(void (*fn)(void)) { return __cxa_atexit((void (*)(void *))fn, 0, &__dso_handle); }

/*
 * Go-owned Python threads. build.sh links libpython with --wrap for pthread_create,
 * pthread_join, pthread_detach and pthread_exit, sending them to the __wrap_ versions
 * below. Once Go installs a spawn hook, Python threads run on locked goroutines: package
 * py's (pythago_py.c, handshake in C) or the sthreads spike's Go-side hooks. Without
 * hooks, or for a non-default stack size (threading.stack_size), libc creates the thread.
 */
#include <errno.h>
#include <pthread.h>

/* start returns the new thread's pthread_t, or 0 on failure. join and detach return
 * 1 when the thread is Go-owned. exit only returns for threads that are not Go-owned. */
static struct {
    uintptr_t (*start)(void *(*fn)(void *), void *arg);
    int (*join)(pthread_t th);
    int (*detach)(pthread_t th);
    void (*exit)(void);
} go_thread_hooks;

void pythago_set_thread_hooks(void *start, void *join, void *detach, void *exit)
{
    go_thread_hooks.start = (uintptr_t (*)(void *(*)(void *), void *))start;
    go_thread_hooks.join = (int (*)(pthread_t))join;
    go_thread_hooks.detach = (int (*)(pthread_t))detach;
    go_thread_hooks.exit = (void (*)(void))exit;
}

uintptr_t pythago_thread_self(void) { return (uintptr_t)pthread_self(); }

static int default_stack(const pthread_attr_t *attr)
{
    pthread_attr_t def;
    size_t want, have;
    if (attr == NULL) {
        return 1;
    }
    if (pthread_attr_init(&def) != 0) {
        return 0;
    }
    int same = pthread_attr_getstacksize(attr, &want) == 0 &&
               pthread_attr_getstacksize(&def, &have) == 0 && want == have;
    pthread_attr_destroy(&def);
    return same;
}

/* Package py (pythago_py.c): goroutine-backed threads with the handshake in C. */
extern int pythago_py_spawn(void *(*fn)(void *), void *arg, pthread_t *out);
extern int pythago_py_join(pthread_t th);
extern int pythago_py_detach(pthread_t th);
extern void pythago_py_exit_current(void);

/* Virtual threads (pythago_vt.c): with spawning enabled, threads start as VTs. */
extern int pythago_vt_spawn(void *(*fn)(void *), void *arg, size_t stack, pthread_t *out);
extern int pythago_vt_join_handle(pthread_t th);
extern int pythago_vt_detach_handle(pthread_t th);
extern int pythago_vt_hang_current(void);

int __wrap_pthread_create(pthread_t *th, const pthread_attr_t *attr,
                           void *(*fn)(void *), void *arg)
{
    size_t stack = 0;
    if (!default_stack(attr)) {
        pthread_attr_getstacksize(attr, &stack); /* threading.stack_size() */
    }
    int r = pythago_vt_spawn(fn, arg, stack, th);
    if (r >= 0) {
        return r;
    }
    if (default_stack(attr) && (r = pythago_py_spawn(fn, arg, th)) >= 0) {
        return r;
    }
    if (go_thread_hooks.start != NULL && default_stack(attr)) {
        uintptr_t t = go_thread_hooks.start(fn, arg);
        if (t == 0) {
            return EAGAIN;
        }
        *th = (pthread_t)t;
        return 0;
    }
    return pthread_create(th, attr, fn, arg);
}

int __wrap_pthread_join(pthread_t th, void **ret)
{
    int vr = pythago_vt_join_handle(th);
    if (vr >= 0) {
        if (vr == 0 && ret != NULL) {
            *ret = NULL;
        }
        return vr;
    }
    if (pythago_py_join(th) == 0) {
        if (ret != NULL) {
            *ret = NULL;
        }
        return 0;
    }
    if (go_thread_hooks.join != NULL && go_thread_hooks.join(th)) {
        if (ret != NULL) {
            *ret = NULL;
        }
        return 0;
    }
    return pthread_join(th, ret);
}

int __wrap_pthread_detach(pthread_t th)
{
    int vr = pythago_vt_detach_handle(th);
    if (vr >= 0) {
        return vr;
    }
    if (pythago_py_detach(th) == 0) {
        return 0;
    }
    if (go_thread_hooks.detach != NULL && go_thread_hooks.detach(th)) {
        return 0;
    }
    return pthread_detach(th);
}

/* pthread_exit would force-unwind through Go frames; Go parks its threads instead. */
void __wrap_pthread_exit(void *ret)
{
    pythago_vt_hang_current(); /* never returns on a VT */
    pythago_py_exit_current(); /* nor on a goroutine-backed thread */
    if (go_thread_hooks.exit != NULL) {
        go_thread_hooks.exit();
    }
    pthread_exit(ret);
}

static int status_result(PyStatus st, const char **err)
{
    if (!PyStatus_Exception(st)) {
        return 0;
    }
    if (err != NULL) {
        *err = st.err_msg != NULL ? st.err_msg : st.func;
    }
    if (PyStatus_IsExit(st)) {
        return st.exitcode != 0 ? st.exitcode : 1;
    }
    return -1;
}

/* pythago_config_new returns an isolated configuration that stops after the core
 * phase (_init_main = 0) and leaves signal handling to Go. The runtime is preinitialized
 * in UTF-8 mode (PEP 540; the default from 3.15, PEP 686): an isolated configuration never
 * sets the locale from the environment, so open() would otherwise default to ASCII. */
PyConfig *pythago_config_new(void)
{
    PyPreConfig pre;
    PyPreConfig_InitIsolatedConfig(&pre);
    pre.utf8_mode = 1;
    if (PyStatus_Exception(Py_PreInitialize(&pre))) {
        return NULL;
    }
    PyConfig *config = PyMem_RawCalloc(1, sizeof(*config));
    if (config == NULL) {
        return NULL;
    }
    PyConfig_InitIsolatedConfig(config);
    config->install_signal_handlers = 0;
    config->_init_main = 0;
    return config;
}

void pythago_config_free(PyConfig *config)
{
    PyConfig_Clear(config);
    PyMem_RawFree(config);
}

int pythago_config_set_home(PyConfig *config, const char *home, const char **err)
{
    return status_result(PyConfig_SetBytesString(config, &config->home, home), err);
}

int pythago_config_set_program_name(PyConfig *config, const char *name, const char **err)
{
    return status_result(PyConfig_SetBytesString(config, &config->program_name, name), err);
}

int pythago_config_append_search_path(PyConfig *config, const char *path, const char **err)
{
    wchar_t *wpath = Py_DecodeLocale(path, NULL);
    if (wpath == NULL) {
        if (err != NULL) {
            *err = "cannot decode module search path";
        }
        return -1;
    }
    config->module_search_paths_set = 1;
    PyStatus st = PyWideStringList_Append(&config->module_search_paths, wpath);
    PyMem_RawFree(wpath);
    return status_result(st, err);
}

/* Let Python install its own signal handlers (SIGINT -> KeyboardInterrupt), as python does.
 * CPython takes SIGINT only if it is still SIG_DFL when its signal module initializes, and
 * Go has installed a handler, so SIGINT goes back to SIG_DFL first. */
#include <signal.h>
void pythago_config_set_signals(PyConfig *config, int on)
{
    config->install_signal_handlers = on;
    if (on) {
        signal(SIGINT, SIG_DFL);
    }
}

int pythago_config_set_executable(PyConfig *config, const char *path, const char **err)
{
    return status_result(PyConfig_SetBytesString(config, &config->executable, path), err);
}

/* pythago_config_set_argv makes CPython parse a python command line (argv[0] is the
 * program), as `python argv[1:]...` would: -c, -m, -I, script paths and so on. */
int pythago_config_set_argv(PyConfig *config, int argc, char *const *argv, const char **err)
{
    config->parse_argv = 1;
    return status_result(PyConfig_SetBytesArgv(config, argc, argv), err);
}

int pythago_initialize_core(PyConfig *config, const char **err)
{
    return status_result(Py_InitializeFromConfig(config), err);
}

/* pythago_initialize_main finishes a core-only initialization. 3.14 removed
 * _Py_InitializeMain; calling Py_InitializeFromConfig again on the core-initialized
 * runtime reconfigures it and, with _init_main set, runs the main phase (3.13 too). */
int pythago_initialize_main(PyConfig *config, const char **err)
{
    config->_init_main = 1;
    return status_result(Py_InitializeFromConfig(config), err);
}
