/*
 * The fusion's reader check (py.Config.FusedBytes; .agents/docs/arena-design.md sections
 * 13 to 15): CPython's own lock-free readers, compiled from its internal headers, run on a
 * pointer the caller chooses. They are not re-implementations: _Py_TryIncrefCompare and
 * _Py_TryIncrefCompareStackRef are the static inline functions that list and dict reads
 * (list_get_item_ref, _Py_dict_lookup_threadsafe, LOAD_GLOBAL) run on free-threaded
 * builds. py.Init runs them once on a freed and on a retaken slot and fails closed unless
 * both refuse the incref without writing; package py's tests run them on every state a
 * slot goes through.
 *
 * Py_BUILD_CORE_MODULE gives the internal headers as a stdlib extension module built as a
 * shared library sees them: no reference to libpython's thread-local thread state, and
 * data imported (Windows' dllimport). This file is its own translation unit everywhere
 * except darwin's unity build (cmd/pythago-dist/darwin.go), which includes it last, after
 * the other glue sources parsed Python.h without it (PYTHAGO_GLUE_UNITY); only the
 * internal headers below then see the core defines, which the functions used here do not
 * otherwise depend on.
 */
#ifndef PYTHAGO_GLUE_UNITY
#define Py_BUILD_CORE_MODULE 1
#ifndef NDEBUG
#define NDEBUG 1 /* a release build's inline functions: no assert() */
#endif
#include <Python.h>
#endif
#ifdef Py_GIL_DISABLED
#ifdef PYTHAGO_GLUE_UNITY
#ifndef Py_BUILD_CORE_MODULE
#define Py_BUILD_CORE_MODULE 1
#endif
#ifndef Py_BUILD_CORE
#define Py_BUILD_CORE 1
#endif
#endif
#include "internal/pycore_object.h"
#include "internal/pycore_stackref.h"
#endif

/* pythago_fusioncheck_tryincref runs _Py_TryIncrefCompare(src, op), as a lock-free reader
 * that loaded op from *src does, and reports in *owned whether op's ob_tid was this
 * thread's id before the call (a stale reader is never the thread that freed or adopted
 * the object it read: callers check that it is not). -1 with the GIL, which has no
 * lock-free readers. */
int pythago_fusioncheck_tryincref(PyObject **src, PyObject *op, int *owned)
{
#ifdef Py_GIL_DISABLED
    *owned = _Py_IsOwnedByCurrentThread(op);
    return _Py_TryIncrefCompare(src, op);
#else
    (void)src;
    (void)op;
    *owned = 0;
    return -1;
#endif
}

/* pythago_fusioncheck_trystackref runs _Py_TryIncrefCompareStackRef(src, op), the stack
 * reference variant (LOAD_GLOBAL, LOAD_ATTR through dict lookups). If it took a reference
 * and close is set, it closes it. -1 with the GIL. */
int pythago_fusioncheck_trystackref(PyObject **src, PyObject *op, int close)
{
#ifdef Py_GIL_DISABLED
    _PyStackRef r;
    int ok = _Py_TryIncrefCompareStackRef(src, op, &r);
    if (ok && close) {
        PyStackRef_CLOSE(r);
    }
    return ok;
#else
    (void)src;
    (void)op;
    (void)close;
    return -1;
#endif
}

/* pythago_fusioncheck_listitem returns &list->ob_item[i]: a reader's src (tests). */
PyObject **pythago_fusioncheck_listitem(PyObject *list, Py_ssize_t i)
{
    return &((PyListObject *)list)->ob_item[i];
}
