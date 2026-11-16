/*
 * pythago C glue: wrappers over C API calls whose signatures purego cannot call
 * directly (PyStatus returned by value) or whose structs Go should not lay out
 * (PyConfig). Every pythago_* function is listed in pythago_symtab. This is the product
 * glue; .agents/attic/spike/glue keeps the frozen spike variant (virtual threads, Go-side
 * thread hooks).
 */
#include <Python.h>

/*
 * Goroutine-backed Python threads. The build wraps pthread_create, pthread_join,
 * pthread_detach and pthread_exit, sending libpython's calls to the __wrap_ versions
 * below, which hand them to package py (pythago_py.c). Threads with a
 * non-default stack size (threading.stack_size), and threads started before py installs
 * its spawn hook or in a forked child, use libc.
 */
#ifndef _WIN32
#include <errno.h>
#include <pthread.h>

/* The build wraps these symbols in libpython and in this glue alike (ld -r --wrap, or bao
 * -wrap with -linux-shared-object), so the system functions are reached as __real_*. */
extern int __real_pthread_create(pthread_t *th, const pthread_attr_t *attr, void *(*fn)(void *), void *arg);
extern int __real_pthread_join(pthread_t th, void **ret);
extern int __real_pthread_detach(pthread_t th);
extern void __real_pthread_exit(void *ret) __attribute__((noreturn));

extern int pythago_py_spawn(void *(*fn)(void *), void *arg, pthread_t *out);
extern int pythago_py_join(pthread_t th);
extern int pythago_py_detach(pthread_t th);
extern void pythago_py_exit_current(void);

static int default_stack(const pthread_attr_t *attr)
{
    pthread_attr_t def;
    size_t want, have;
    if (attr == NULL) {
        return 1;
    }
#if defined(__APPLE__) && defined(THREAD_STACK_SIZE)
    /* CPython on macOS asks every thread for THREAD_STACK_SIZE (pyconfig.h: 16 MiB), since
     * the system default for secondary threads is 512 KiB. A goroutine-backed thread runs
     * on a Go thread, which inherits the main thread's 8 MiB: Linux's default, enough for
     * CPython's recursion limits. That request counts as the default too. */
    if (pthread_attr_getstacksize(attr, &want) == 0 && want == THREAD_STACK_SIZE) {
        return 1;
    }
#endif
    if (pthread_attr_init(&def) != 0) {
        return 0;
    }
    int same = pthread_attr_getstacksize(attr, &want) == 0 &&
               pthread_attr_getstacksize(&def, &have) == 0 && want == have;
    pthread_attr_destroy(&def);
    return same;
}

int __wrap_pthread_create(pthread_t *th, const pthread_attr_t *attr,
                           void *(*fn)(void *), void *arg)
{
    int r;
    if (default_stack(attr) && (r = pythago_py_spawn(fn, arg, th)) >= 0) {
        return r;
    }
    return __real_pthread_create(th, attr, fn, arg);
}

int __wrap_pthread_join(pthread_t th, void **ret)
{
    if (pythago_py_join(th) == 0) {
        if (ret != NULL) {
            *ret = NULL;
        }
        return 0;
    }
    return __real_pthread_join(th, ret);
}

int __wrap_pthread_detach(pthread_t th)
{
    if (pythago_py_detach(th) == 0) {
        return 0;
    }
    return __real_pthread_detach(th);
}

/* pthread_exit would force-unwind through Go frames; goroutine-backed threads park. */
void __wrap_pthread_exit(void *ret)
{
    pythago_py_exit_current(); /* never returns on a goroutine-backed thread */
    __real_pthread_exit(ret);
}
#endif /* !_WIN32: on Windows CPython's own threads run Python (no wraps) */

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
 * sets the locale from the environment, so open() would otherwise default to ASCII.
 * Given a python command line (py.Main; argc 0 otherwise), preinitialization reads it as
 * python's does: -X dev decides there, with the memory allocators' debug hooks. */
PyConfig *pythago_config_new(int argc, char **argv)
{
    PyPreConfig pre;
    PyPreConfig_InitIsolatedConfig(&pre);
    pre.utf8_mode = 1;
    PyStatus st;
    if (argc > 0) {
        pre.parse_argv = 1;
        pre.dev_mode = -1;
        st = Py_PreInitializeFromBytesArgs(&pre, argc, argv);
    } else {
        st = Py_PreInitialize(&pre);
    }
    if (PyStatus_Exception(st)) {
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
 * program), as `python argv[1:]...` would: -c, -m, -I, script paths and so on. An isolated
 * configuration fixes the fields below, and CPython reads their -X options only while they
 * are -1: let the command line decide them, as python -I does (-X dev, faulthandler (3.13),
 * tracemalloc, perf, perf_jit, int_max_str_digits; PYTHON* variables stay ignored).
 * tracemalloc starts in the main phase, after the fusion's hook (py.Config.FusedBytes). */
int pythago_config_set_argv(PyConfig *config, int argc, char *const *argv, const char **err)
{
    config->parse_argv = 1;
    config->dev_mode = -1;
    config->faulthandler = -1;
    config->tracemalloc = -1;
    config->perf_profiling = -1;
    config->int_max_str_digits = -1;
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
