// Command smn has many goroutines call into Python over few OS threads (M:N).
//
//	PYTHONHOME=<pbs install> smn
//
// Each call locks the goroutine to its current OS thread only for the duration of
// the call. Goroutines first take a slot in a Go-side admission semaphore: a goroutine
// waiting for the GIL inside C holds an OS thread, so without the semaphore every
// waiting goroutine becomes an OS thread (10000 goroutines hit Go's thread limit).
// Two thread-state strategies are compared:
//
//	gilstate: PyGILState_Ensure/Release per call; the thread state is created and
//	          destroyed every call because nothing else holds it.
//	cached:   one PyThreadState per OS thread, created lazily with PyThreadState_New
//	          and entered with PyEval_RestoreThread/SaveThread.
//
// A third run has goroutines block inside Python (time.sleep) to show that a blocked
// Python call holds its OS thread.
package main

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/moriyoshi/pythago/internal/spike/pyapi"
	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

func init() { runtime.LockOSThread() }

var (
	pyInitializeEx       func(int32)
	pyFinalizeEx         func() int32
	pyRunSimpleString    func(string) int32
	pyImportAddModule    func(string) uintptr
	pyObjectGetAttrStr   func(uintptr, string) uintptr
	pyObjectCallNoArgs   func(uintptr) uintptr
	pyDecRef             func(uintptr)
	pyEvalSaveThread     func() uintptr
	pyEvalRestoreThread  func(uintptr)
	pyGILStateEnsure     func() int32
	pyGILStateRelease    func(int32)
	pyThreadStateNew     func(uintptr) uintptr
	pyInterpreterStateMn func() uintptr
	pyErrPrint           func()
)

var tstates sync.Map // OS thread id -> *PyThreadState

// admit bounds how many goroutines are inside Python (running, waiting for the GIL, or
// blocked with it released) and so how many OS threads Go→Python calls occupy.
var admit chan struct{}

func enter() func() {
	admit <- struct{}{}
	return func() { <-admit }
}

func doGILState(f func()) {
	defer enter()()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	g := pyGILStateEnsure()
	f()
	pyGILStateRelease(g)
}

func doCached(f func()) {
	defer enter()()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tid := syscall.Gettid()
	ts, ok := tstates.Load(tid)
	if !ok {
		// Binds the new thread state (and the GILState mapping) to this OS thread.
		ts = pyThreadStateNew(pyInterpreterStateMn())
		tstates.Store(tid, ts)
	}
	pyEvalRestoreThread(ts.(uintptr))
	f()
	pyEvalSaveThread()
}

func osThreads() int {
	tasks, _ := os.ReadDir("/proc/self/task")
	return len(tasks)
}

func call(fn uintptr) {
	r := pyObjectCallNoArgs(fn)
	if r == 0 {
		pyErrPrint()
		os.Exit(1)
	}
	pyDecRef(r)
}

func main() {
	pyapi.Load(pyembed.SymbolAddr0())
	for _, f := range []struct {
		p    any
		name string
	}{
		{&pyInitializeEx, "Py_InitializeEx"}, {&pyFinalizeEx, "Py_FinalizeEx"},
		{&pyRunSimpleString, "PyRun_SimpleString"}, {&pyImportAddModule, "PyImport_AddModule"},
		{&pyObjectGetAttrStr, "PyObject_GetAttrString"}, {&pyObjectCallNoArgs, "PyObject_CallNoArgs"},
		{&pyDecRef, "Py_DecRef"}, {&pyEvalSaveThread, "PyEval_SaveThread"},
		{&pyEvalRestoreThread, "PyEval_RestoreThread"}, {&pyGILStateEnsure, "PyGILState_Ensure"},
		{&pyGILStateRelease, "PyGILState_Release"}, {&pyThreadStateNew, "PyThreadState_New"},
		{&pyInterpreterStateMn, "PyInterpreterState_Main"}, {&pyErrPrint, "PyErr_Print"},
	} {
		pyapi.Register(f.p, f.name)
	}
	pyInitializeEx(0)
	pyRunSimpleString(`
import threading, time
local = threading.local()
idents, reused, calls = set(), 0, 0
def work():
    global reused, calls
    calls += 1
    idents.add(threading.get_ident())
    n = getattr(local, "n", 0)
    reused += n > 0
    local.n = n + 1
def pure():
    return sum(range(2000))  # No shared mutable state.
def nap():
    time.sleep(0.05)
def report(label):
    global idents, reused, calls
    print(f"  {label}: {calls} calls, {len(idents)} distinct Python thread idents, "
          f"threading.local survived in {reused} calls")
    idents, reused, calls = set(), 0, 0
`)
	mainMod := pyImportAddModule("__main__")
	work := pyObjectGetAttrStr(mainMod, "work")
	if os.Getenv("SMN_PURE") != "" {
		pyDecRef(work)
		work = pyObjectGetAttrStr(mainMod, "pure")
	}
	nap := pyObjectGetAttrStr(mainMod, "nap")
	report := func(label string) {
		pyRunSimpleString(fmt.Sprintf("report(%q)", label))
	}
	mainTS := pyEvalSaveThread()

	const goroutines, perG = 10000, 10
	slots := runtime.GOMAXPROCS(0)
	if v := os.Getenv("SMN_SLOTS"); v != "" {
		fmt.Sscan(v, &slots)
	}
	admit = make(chan struct{}, slots)
	fmt.Printf("GOMAXPROCS=%d, admission slots %d, %d goroutines x %d calls\n", runtime.GOMAXPROCS(0), slots, goroutines, perG)
	for _, mode := range []struct {
		name string
		do   func(func())
	}{{"gilstate", doGILState}, {"cached", doCached}} {
		start := time.Now()
		var wg sync.WaitGroup
		for range goroutines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range perG {
					mode.do(func() { call(work) })
				}
			}()
		}
		wg.Wait()
		dt := time.Since(start)
		fmt.Printf("%s: %v (%.0f ns/call), OS threads %d\n", mode.name, dt.Round(time.Millisecond),
			float64(dt.Nanoseconds())/goroutines/perG, osThreads())
		pyEvalRestoreThread(mainTS)
		report(mode.name)
		mainTS = pyEvalSaveThread()
	}

	// Goroutines blocking inside Python each hold an OS thread for the duration.
	const sleepers = 500
	peak := 0
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				peak = max(peak, osThreads())
			}
		}
	}()
	start := time.Now()
	var wg sync.WaitGroup
	for range sleepers {
		wg.Add(1)
		go func() { defer wg.Done(); doCached(func() { call(nap) }) }()
	}
	wg.Wait()
	close(stop)
	fmt.Printf("blocking: %d goroutines in time.sleep(0.05) took %v, peak OS threads %d\n",
		sleepers, time.Since(start).Round(time.Millisecond), peak)

	pyEvalRestoreThread(mainTS)
	pyDecRef(work)
	pyDecRef(nap)
	if pyFinalizeEx() != 0 {
		fmt.Fprintln(os.Stderr, "Py_FinalizeEx failed")
		os.Exit(1)
	}
}
