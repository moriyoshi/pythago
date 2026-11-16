/* Shared between pythago_vt.c (virtual threads) and pythago_vtio.c (VT-aware waits). */
#ifndef PYTHAGO_VT_H
#define PYTHAGO_VT_H

#include <stdatomic.h>
#include <pthread.h>
#include <stdint.h>

/* PYTHAGO_NO_VT (build.sh NO_VT=1) leaves virtual threads out: package py runs Python
 * threads on goroutines and needs none of the VT wrappers. */
#if PY_VERSION_HEX >= 0x030E0000 && defined(__aarch64__) && !defined(PYTHAGO_NO_VT)
#define PYTHAGO_VT 1
#endif

/* VT_HUNG: a VT that must never run again (CPython hangs daemon threads at shutdown). */
enum { VT_NEW, VT_RUNNING, VT_SLEEP, VT_DONE, VT_PARK, VT_HUNG };

typedef struct pythago_vtev pythago_vtev;

typedef struct pythago_vt {
    /* Persistent across reuse: VT structs are never freed, only recycled through a slot
     * table, so a stale pointer or token can never reach freed memory. */
    uint32_t idx;            /* slot in the VT table */
    _Atomic uint64_t gen;    /* incarnation; bumped when the VT is released */
    _Atomic uint64_t park;   /* park sequence << 1 | parked flag; never reset */
    /* Reset on every allocation (from here on): */
    pthread_t ident;         /* this incarnation's pthread_self()/get_ident() token */
    void *sp;          /* VT context while suspended */
    void *carrier_sp;  /* carrier context while the VT runs */
    char *stack;
    size_t stack_size;
    PyObject *callable;
    long long arg;
    PyThreadState *tstate;
    int state;
    int pinned;        /* inside a Go callback: blocking calls block the carrier */
    int home;          /* carrier index, for wakers */
    int64_t wake_ns;   /* CLOCK_MONOTONIC deadline for VT_SLEEP/VT_PARK, or -1 */
    struct pythago_vt *next_ready;
    /* Threads spawned through pthread_create (threading.Thread): */
    void *(*fn)(void *);  /* entry point instead of callable */
    void *fnarg;
    _Atomic int refs;      /* the thread itself + its pthread_t handle (join/detach) */
    _Atomic int finished;
    pythago_vtev *done_ev; /* joiners wait here */
    char name[16];         /* pthread_setname_np on a VT (threading names threads in 3.14) */
    int kind;              /* VT_KIND_* */
    int pooled;            /* runs on a thread state Go handed it (pythago_vt_set_tstate) */
    /* A VT that reached a Go builtin during a nested run waits here (vt__nest_one): */
    uint64_t defer_tok;
    struct pythago_vt *next_defer;
} pythago_vt;

#define VT_RESET_FROM ident

/* Identity tokens (pthread_t of a VT): (gen << 25) | (idx << 1) | 1. Odd, so never a glibc
 * thread pointer; unique per incarnation, so a finished thread's ident is never reused;
 * resolved through the slot table, never dereferenced. */
#define VT_IDX_BITS 24
#define VT_IDX_MAX ((1u << VT_IDX_BITS) - 2)
pythago_vt *vt__by_ident(pthread_t th);

/* Park tokens (what wakers hold): ((idx + 1) << 24) | (park sequence & 0xffffff). Never 0
 * or 1, fits 48 bits (a semaphore's waiter field). A waker whose token no longer matches
 * the VT's current park does nothing. */
uint64_t vt__park_begin(pythago_vt *vt);   /* sets parked; returns this park's token */
int vt__unpark_self(pythago_vt *vt);       /* cancels own park; 0 if a waker took it */
pythago_vt *vt__unpark_tok(uint64_t tok);  /* claims the wake; NULL if stale/already woken */
void vt__wake_tok(uint64_t tok);           /* unpark + enqueue on the home carrier */


/* VT_KIND_TASK: created by Go (it frees it). VT_KIND_PTHREAD: from pthread_create, CPython
 * owns the thread state. VT_KIND_PYTHON: threading.Thread through _pythago_vt.spawn, on a
 * pooled thread state. The last two free themselves when their references drop. */
enum { VT_KIND_TASK, VT_KIND_PTHREAD, VT_KIND_PYTHON };

pythago_vt *vt__current(void);
void vt__yield(pythago_vt *vt, int state);
int64_t vt__monotonic_ns(void);
int vt__may_park(pythago_vt *vt);
int64_t vt__block_recheck_ns(void); /* how soon a VT that must block re-checks */
int vt__may_park_ex(pythago_vt *vt, int ignore_pin);
int64_t vt__block_slice(pythago_vt *vt); /* recheck period for vt, nesting included */
int vt__nest_one(pythago_vt *vt);        /* see pythago_vt.c */
void vt__enqueue_new(pythago_vt *vt);
pythago_vtev *pythago_vtev_new(void);
void pythago_vtev_set(pythago_vtev *e);
uint32_t vt__ev_seq(pythago_vtev *e);
void vt__ev_wait(pythago_vtev *e, uint32_t seq, int64_t deadline);

#endif
