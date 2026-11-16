/*
 * Glue that only the static-archive build (LTO-lowered libpython.a) needs: definitions
 * that the PBS link gets from glibc 2.17's static libc_nonshared.a. A shared libpython
 * (bao -linux-shared-object) already has them resolved, so that build leaves this file out.
 */
#define _GNU_SOURCE /* struct stat64 */

/* glibc keeps atexit in the static libc_nonshared.a; libc.so.6 only exports
 * __cxa_atexit. bao's __dso_handle identifies the translated image. */
extern int __cxa_atexit(void (*fn)(void *), void *arg, void *dso);
extern char __dso_handle;
int atexit(void (*fn)(void)) { return __cxa_atexit((void (*)(void *))fn, 0, &__dso_handle); }

/*
 * glibc before 2.33 provided stat64, fstat64 and lstat64 as static wrappers in
 * libc_nonshared.a, calling __xstat64 and friends; libc.so.6 exports them only from
 * GLIBC_2.33. The PBS sqlite references them directly (PBS links against glibc 2.17's
 * libc_nonshared.a), so define the same wrappers here: the imports then stay within the
 * image's glibc floor (__xstat64@GLIBC_2.17 on aarch64, GLIBC_2.2.5 on x86_64), which
 * newer glibc keeps exporting for compatibility.
 */
#include <sys/stat.h>
#if defined(__x86_64__)
#define PYTHAGO_STAT_VER 1 /* _STAT_VER_LINUX */
#else
#define PYTHAGO_STAT_VER 0 /* _STAT_VER on the generic (aarch64) ABI */
#endif
extern int __xstat64(int ver, const char *path, struct stat64 *buf);
extern int __fxstat64(int ver, int fd, struct stat64 *buf);
extern int __lxstat64(int ver, const char *path, struct stat64 *buf);
int stat64(const char *path, struct stat64 *buf) { return __xstat64(PYTHAGO_STAT_VER, path, buf); }
int fstat64(int fd, struct stat64 *buf) { return __fxstat64(PYTHAGO_STAT_VER, fd, buf); }
int lstat64(const char *path, struct stat64 *buf) { return __lxstat64(PYTHAGO_STAT_VER, path, buf); }
