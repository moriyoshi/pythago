// Command sthreads runs Python threads on Go-created OS threads (locked goroutines)
// through the glue's pythago_pthread_* hooks, and compares Python→Go callback cost
// against libc-created threads.
//
//	PYTHONHOME=<pbs install> sthreads
//
// STHREADS_OFF=1 leaves thread creation to libc.
package main

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/moriyoshi/pythago/internal/spike/pyapi"
	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

func init() { runtime.LockOSThread() }

var (
	pyInitializeEx     func(int32)
	pyFinalizeEx       func() int32
	pyRunSimpleString  func(string) int32
	pyLongFromLongLong func(int64) uintptr
	pyCMethodNew       func(def, self, module, cls uintptr) uintptr
	pyImportModule     func(string) uintptr
	pyObjectSetAttrStr func(obj uintptr, name string, v uintptr) int32
	pyDecRef           func(uintptr)
	pyIncRef           func(uintptr)
	pyMemRawCalloc     func(n, size uintptr) uintptr
	setThreadHooks     func(start, join, detach, exit uintptr)
	threadSelf         func() uintptr
)

// goThread is a Python thread running on a locked goroutine. Like a joinable
// pthread, it keeps its OS thread (and so its pthread_t) until joined or detached.
type goThread struct {
	done    chan struct{}
	release chan struct{}
}

var (
	threadsMu sync.Mutex
	threads   = map[uintptr]*goThread{}
	goTIDs    sync.Map // OS thread ids that Go created for Python.
	started   atomic.Int32
)

func threadStart(fn, arg uintptr) uintptr {
	ch := make(chan uintptr)
	go func() {
		// Never unlocked: the OS thread exits with this goroutine, running pthread
		// TLS destructors as a libc-created thread would.
		runtime.LockOSThread()
		t := threadSelf()
		gt := &goThread{done: make(chan struct{}), release: make(chan struct{})}
		threadsMu.Lock()
		threads[t] = gt
		threadsMu.Unlock()
		goTIDs.Store(syscall.Gettid(), true)
		started.Add(1)
		ch <- t
		purego.SyscallN(fn, arg)
		close(gt.done)
		<-gt.release
	}()
	return <-ch
}

func take(th uintptr) *goThread {
	threadsMu.Lock()
	defer threadsMu.Unlock()
	gt := threads[th]
	delete(threads, th)
	return gt
}

func threadJoin(th uintptr) uintptr {
	gt := take(th)
	if gt == nil {
		return 0
	}
	<-gt.done
	close(gt.release)
	return 1
}

func threadDetach(th uintptr) uintptr {
	gt := take(th)
	if gt == nil {
		return 0
	}
	close(gt.release)
	return 1
}

// threadExit parks a Go-owned thread that Python wants to pthread_exit (daemon
// threads during finalization); unwinding through Go frames is not possible.
func threadExit() {
	threadsMu.Lock()
	_, ours := threads[threadSelf()]
	threadsMu.Unlock()
	if ours {
		select {}
	}
}

var (
	goCalls   atomic.Int64
	goOwned   atomic.Int64
	callerTID sync.Map
)

// dispatch backs the go_noop builtin (METH_FASTCALL|METH_KEYWORDS).
func dispatch(self, args uintptr, nargs int, kwnames uintptr) uintptr {
	goCalls.Add(1)
	tid := syscall.Gettid()
	callerTID.Store(tid, true)
	if _, ok := goTIDs.Load(tid); ok {
		goOwned.Add(1)
	}
	none := pyapi.Addr("_Py_NoneStruct")
	pyIncRef(none)
	return none
}

func main() {
	pyapi.Load(pyembed.SymbolAddr0())
	for _, f := range []struct {
		p    any
		name string
	}{
		{&pyInitializeEx, "Py_InitializeEx"}, {&pyFinalizeEx, "Py_FinalizeEx"},
		{&pyRunSimpleString, "PyRun_SimpleString"}, {&pyLongFromLongLong, "PyLong_FromLongLong"},
		{&pyCMethodNew, "PyCMethod_New"}, {&pyImportModule, "PyImport_ImportModule"},
		{&pyObjectSetAttrStr, "PyObject_SetAttrString"}, {&pyDecRef, "Py_DecRef"},
		{&pyIncRef, "Py_IncRef"}, {&pyMemRawCalloc, "PyMem_RawCalloc"},
		{&setThreadHooks, "pythago_set_thread_hooks"}, {&threadSelf, "pythago_thread_self"},
	} {
		pyapi.Register(f.p, f.name)
	}
	mode := "go"
	if os.Getenv("STHREADS_OFF") == "" {
		setThreadHooks(purego.NewCallback(threadStart), purego.NewCallback(threadJoin),
			purego.NewCallback(threadDetach), purego.NewCallback(threadExit))
	} else {
		mode = "libc"
	}
	pyInitializeEx(0)

	name := "go_noop"
	cname := pyMemRawCalloc(uintptr(len(name)+1), 1)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(cname)), len(name)), name)
	def := pyMemRawCalloc(1, 32)
	d := (*[4]uintptr)(unsafe.Pointer(def))
	d[0], d[1], d[2] = cname, purego.NewCallback(dispatch), 0x82
	fn := pyCMethodNew(def, 0, 0, 0)
	builtins := pyImportModule("builtins")
	pyObjectSetAttrStr(builtins, name, fn)
	pyDecRef(fn)
	pyDecRef(builtins)

	rc := pyRunSimpleString(`
import threading, _thread, time, concurrent.futures as cf
mode = "` + mode + `"

# Identity: the ident Python recorded at start matches get_ident() inside the thread.
seen = {}
def ident_probe():
    seen[threading.current_thread().name] = (threading.get_ident(), threading.get_native_id())
ts = [threading.Thread(target=ident_probe, name=f"t{i}") for i in range(8)]
[t.start() for t in ts]; [t.join() for t in ts]
assert all(seen[t.name] == (t.ident, t.native_id) for t in ts), (seen, [(t.ident, t.native_id) for t in ts])

# Join with timeout, is_alive, start_new_thread (detached), executor, Event.
ev = threading.Event()
t = threading.Thread(target=ev.wait); t.start()
t.join(0.05); assert t.is_alive()
ev.set(); t.join(); assert not t.is_alive()
lock = _thread.allocate_lock(); lock.acquire()
_thread.start_new_thread(lambda: (go_noop(), lock.release()), ())
assert lock.acquire(timeout=5)
with cf.ThreadPoolExecutor(8) as ex:
    assert sum(ex.map(lambda x: x * x, range(1000))) == sum(x * x for x in range(1000))
threading.stack_size(1 << 20)                       # Non-default stack: libc path.
t = threading.Thread(target=go_noop); t.start(); t.join()
threading.stack_size(0)

# Callback throughput from Python threads (GIL-serialized).
N, T = 20000, 8
def spin():
    for _ in range(N):
        go_noop()
ts = [threading.Thread(target=spin) for _ in range(T)]
t0 = time.perf_counter()
[t.start() for t in ts]; [t.join() for t in ts]
dt = time.perf_counter() - t0
print(f"{mode}: {N*T} callbacks from {T} threads in {dt*1e3:.0f} ms ({dt/(N*T)*1e9:.0f} ns/call)")

# A daemon thread still running at finalization.
def forever():
    while True:
        time.sleep(0.001)
threading.Thread(target=forever, daemon=True).start()
`)
	tasks, _ := os.ReadDir("/proc/self/task")
	tids := 0
	callerTID.Range(func(any, any) bool { tids++; return true })
	fmt.Printf("%s: python threads started on Go threads: %d; callbacks %d (%d on Go-owned threads) from %d OS threads; goroutines %d; live OS threads %d\n",
		mode, started.Load(), goCalls.Load(), goOwned.Load(), tids, runtime.NumGoroutine(), len(tasks))
	if pyFinalizeEx() != 0 {
		fmt.Fprintln(os.Stderr, "Py_FinalizeEx failed")
		os.Exit(1)
	}
	fmt.Println("finalized")
	os.Exit(int(rc))
}
