package goaio

import "github.com/moriyoshi/pythago/internal/spike/pyapi"

// vtThreadsSource rebinds threading's low-level hooks so threading.Thread (and
// _thread.start_new_thread) start virtual threads on pooled thread states. It replaces
// only what threading.py binds from _thread at import time; CPython is unchanged.
const vtThreadsSource = `
import _thread, sys, threading, traceback
import _pythago_vt

class _VTHandle:
    """threading's per-thread handle (ident, is_done, join, _set_done) for a VT."""
    __slots__ = ("ident", "_lock", "__weakref__")

    def __init__(self):
        self.ident = None
        self._lock = _thread.allocate_lock()   # held until the thread is done
        self._lock.acquire()

    def is_done(self):
        return not self._lock.locked()

    def _set_done(self):
        if self._lock.locked():
            self._lock.release()

    def join(self, timeout=None):
        if self.ident is not None and self.ident == _thread.get_ident():
            raise RuntimeError("Cannot join current thread")
        if timeout is None or timeout < 0:
            self._lock.acquire()
            self._lock.release()
        elif self._lock.acquire(timeout=timeout):
            self._lock.release()

_nondaemon = set()
_nondaemon_lock = _thread.allocate_lock()

def _start_joinable_thread(function, handle=None, daemon=True):
    if handle is None:
        handle = _VTHandle()
    if not daemon:
        with _nondaemon_lock:
            _nondaemon.add(handle)
    def entry(_):
        try:
            function()
        finally:
            handle._set_done()
            if not daemon:
                with _nondaemon_lock:
                    _nondaemon.discard(handle)
    handle.ident = _pythago_vt.spawn(entry)
    return handle

_native_shutdown = threading._thread_shutdown

def _thread_shutdown():
    _native_shutdown()
    while True:           # non-daemon VT threads, as _thread._shutdown does for its own
        with _nondaemon_lock:
            pending = list(_nondaemon)
        if not pending:
            break
        for handle in pending:
            handle.join()

def start_new_thread(function, args, kwargs=None):
    def run():
        try:
            function(*args, **(kwargs or {}))
        except SystemExit:
            pass
        except BaseException:
            traceback.print_exc()
    return _start_joinable_thread(run, daemon=True).ident

threading._ThreadHandle = _VTHandle
threading._start_joinable_thread = _start_joinable_thread
threading._thread_shutdown = _thread_shutdown
_thread.start_new_thread = _thread.start_new = start_new_thread
`

// InstallVTThreads makes threading.Thread run on virtual threads with pooled thread
// states. Virtual-thread spawning must be enabled (pythago_vt_set_threads).
func InstallVTThreads() {
	Init()
	mod := pyModuleNew("_pythago_vt")
	fn := cBuiltin("spawn", "pythago_vt_spawn_py")
	pyObjectSetAttrStr(mod, "spawn", fn)
	pyDecRef(fn)
	pyDictSetItemString(pyImportGetModuleDict(), "_pythago_vt", mod)
	pyDecRef(mod)
	code := pyCompileString(vtThreadsSource, "<pythago-vt-threads>", 257)
	if code == 0 || pyImportExecCodeModule("_pythago_vt_threads", code) == 0 {
		pyErrPrint()
		panic("goaio: cannot install VT threads")
	}
	pyDecRef(code)
	_ = pyapi.Symbols
}
