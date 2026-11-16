/*
 * Windows: the few pthread and POSIX calls the glue makes, on Win32. On Windows CPython
 * creates its own threads (the glue does not wrap them), so the goroutine-backed thread
 * code in pythago_py.c compiles but is never entered; what runs is the per-OS-thread
 * thread state cache (keys, once) and the locks.
 */
#ifndef PYTHAGO_WIN32_H
#define PYTHAGO_WIN32_H

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <errno.h>
#undef small /* rpcndr.h's #define small char; the glue names variables small */

typedef DWORD pthread_key_t;
typedef INIT_ONCE pthread_once_t;
typedef SRWLOCK pthread_mutex_t;
typedef DWORD pthread_t;

#define PTHREAD_ONCE_INIT INIT_ONCE_STATIC_INIT
#define PTHREAD_MUTEX_INITIALIZER SRWLOCK_INIT

/* A fiber-local slot: its callback runs at thread exit with the slot's value, as a
 * pthread key destructor does (and not for NULL). */
static inline int pthread_key_create(pthread_key_t *key, void (*dtor)(void *))
{
    DWORD k = FlsAlloc((PFLS_CALLBACK_FUNCTION)dtor);
    if (k == FLS_OUT_OF_INDEXES) {
        return EAGAIN;
    }
    *key = k;
    return 0;
}

static inline int pthread_setspecific(pthread_key_t key, const void *value)
{
    return FlsSetValue(key, (void *)value) ? 0 : EINVAL;
}

static BOOL CALLBACK pythago_once_run(PINIT_ONCE once, PVOID fn, PVOID *ctx)
{
    (void)once;
    (void)ctx;
    ((void (*)(void))fn)();
    return TRUE;
}

static inline int pthread_once(pthread_once_t *once, void (*fn)(void))
{
    return InitOnceExecuteOnce(once, pythago_once_run, (PVOID)fn, NULL) ? 0 : EINVAL;
}

static inline int pthread_mutex_init(pthread_mutex_t *mu, void *attr)
{
    (void)attr;
    InitializeSRWLock(mu);
    return 0;
}

static inline int pthread_mutex_lock(pthread_mutex_t *mu)
{
    AcquireSRWLockExclusive(mu);
    return 0;
}

static inline int pthread_mutex_unlock(pthread_mutex_t *mu)
{
    ReleaseSRWLockExclusive(mu);
    return 0;
}

static inline pthread_t pthread_self(void) { return GetCurrentThreadId(); }
static inline int pthread_equal(pthread_t a, pthread_t b) { return a == b; }
static inline int sched_yield(void) { return SwitchToThread(), 0; }

/*
 * The TLS directory a DLL with implicit thread-local variables (the glue's __thread ones)
 * needs, which an MSVC or MinGW CRT would supply as tlssup: the loader gives each thread
 * a block initialized from the .tls template between _tls_start and _tls_end, and stores
 * its index in _tls_index. The glue DLL links no CRT startup code, so it is here, in the
 * one file that includes this header.
 */
typedef void (*pythago_tls_callback)(void *, unsigned long, void *);
unsigned int _tls_index = 0;
#pragma section(".tls", read, write)
#pragma section(".tls$ZZZ", read, write)
__declspec(allocate(".tls")) char _tls_start = 0;
__declspec(allocate(".tls$ZZZ")) char _tls_end = 0;
#pragma section(".CRT$XLA", read)
#pragma section(".CRT$XLZ", read)
__declspec(allocate(".CRT$XLA")) pythago_tls_callback __xl_a = 0;
__declspec(allocate(".CRT$XLZ")) pythago_tls_callback __xl_z = 0;
#pragma section(".rdata$T", read)
__declspec(allocate(".rdata$T")) const IMAGE_TLS_DIRECTORY64 _tls_used = {
    (ULONGLONG)&_tls_start, (ULONGLONG)&_tls_end, (ULONGLONG)&_tls_index,
    (ULONGLONG)(&__xl_a + 1), 0, 0,
};

/* x64 code that uses floating point references _fltused, which the CRT's startup objects
 * would define. */
#if defined(_M_X64) || defined(_M_IX86)
int _fltused = 0;
#endif

#endif
