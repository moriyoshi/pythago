/*
 * Test-only LD_PRELOAD shim for running linux/amd64 builds under qemu-user 8.2.2 on an
 * arm64 host, which crashes ("QEMU internal SIGSEGV", addr 0x20) whenever the guest reads
 * /proc/self/maps, even from a plain Go program. Opening it fails with ENOENT instead (CPython's
 * path configuration copes), and pthread_getattr_np on the main thread, which glibc
 * answers from /proc/self/maps internally (3.14's _Py_InitializeRecursionLimits), is
 * answered from __libc_stack_end and RLIMIT_STACK.
 *
 *   clang --target=x86_64-linux-gnu --sysroot=$SYSROOT -fuse-ld=lld -shared -fPIC -O1 \
 *     -B$SYSROOT/usr/lib/gcc/x86_64-linux-gnu/13 -L$SYSROOT/usr/lib/gcc/x86_64-linux-gnu/13 \
 *     -o $SYSROOT/nomaps.so nomaps.c
 *   QEMU_LD_PREFIX=$SYSROOT QEMU_SET_ENV=LD_PRELOAD=/nomaps.so PYTHONHOME=... ./binary
 *
 * Link it with a version script giving these symbols glibc's base version (GLIBC_2.2.5 on
 * x86_64, GLIBC_2.17 on aarch64; ci/lib.sh does): bao binds a shared libpython's imports
 * with dlvsym(RTLD_DEFAULT, name, version), which ignores unversioned definitions.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static int is_maps(const char *p) { return p && (!strcmp(p, "/proc/self/maps") || !strncmp(p, "/proc/", 6) && strstr(p, "/maps")); }
static void note(const char *fn, void *ra)
{
    char buf[96];
    int n = snprintf(buf, sizeof buf, "nomaps: %s(/proc/self/maps) from %p\n", fn, ra);
    write(2, buf, n);
}
#define REFUSE(fn) do { note(fn, __builtin_return_address(0)); errno = ENOENT; } while (0)

int open(const char *p, int f, ...)
{
    va_list a; va_start(a, f); int m = va_arg(a, int); va_end(a);
    if (is_maps(p)) { REFUSE("open"); return -1; }
    static int (*real)(const char *, int, ...); if (!real) real = dlsym(RTLD_NEXT, "open");
    return real(p, f, m);
}
int open64(const char *p, int f, ...)
{
    va_list a; va_start(a, f); int m = va_arg(a, int); va_end(a);
    if (is_maps(p)) { REFUSE("open64"); return -1; }
    static int (*real)(const char *, int, ...); if (!real) real = dlsym(RTLD_NEXT, "open64");
    return real(p, f, m);
}
int openat(int d, const char *p, int f, ...)
{
    va_list a; va_start(a, f); int m = va_arg(a, int); va_end(a);
    if (is_maps(p)) { REFUSE("openat"); return -1; }
    static int (*real)(int, const char *, int, ...); if (!real) real = dlsym(RTLD_NEXT, "openat");
    return real(d, p, f, m);
}
FILE *fopen(const char *p, const char *m)
{
    if (is_maps(p)) { REFUSE("fopen"); return NULL; }
    static FILE *(*real)(const char *, const char *); if (!real) real = dlsym(RTLD_NEXT, "fopen");
    return real(p, m);
}
FILE *fopen64(const char *p, const char *m)
{
    if (is_maps(p)) { REFUSE("fopen64"); return NULL; }
    static FILE *(*real)(const char *, const char *); if (!real) real = dlsym(RTLD_NEXT, "fopen64");
    return real(p, m);
}
#include <pthread.h>
#include <sys/resource.h>
#include <sys/syscall.h>
extern void *__libc_stack_end;
/* glibc reads /proc/self/maps for the main thread's stack; answer from __libc_stack_end and
 * RLIMIT_STACK instead (test-only approximation). */
int pthread_getattr_np(pthread_t th, pthread_attr_t *attr)
{
    static int (*real)(pthread_t, pthread_attr_t *);
    if (!real) real = dlsym(RTLD_NEXT, "pthread_getattr_np");
    if (syscall(SYS_gettid) == getpid() && pthread_equal(th, pthread_self())) {
        struct rlimit rl;
        getrlimit(RLIMIT_STACK, &rl);
        size_t size = rl.rlim_cur == RLIM_INFINITY || rl.rlim_cur > (64u << 20) ? (8u << 20) : rl.rlim_cur;
        unsigned long top = ((unsigned long)__libc_stack_end + 4095) & ~4095ul;
        pthread_attr_init(attr);
        pthread_attr_setstack(attr, (void *)(top - size), size);
        return 0;
    }
    return real(th, attr);
}
