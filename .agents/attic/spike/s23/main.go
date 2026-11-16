// Command s23 checks the pythago_symtab (S2) and Python→Go callbacks (S3).
//
// It exposes Go functions to Python as METH_FASTCALL builtins dispatched through a
// single purego callback, calls them from the main thread and Python-created
// threads, propagates a Go error as a Python exception, and has goroutines enter
// Python through PyGILState_Ensure.
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
	pyInitializeEx       func(int32)
	pyFinalizeEx         func() int32
	pyRunSimpleString    func(string) int32
	pyEvalSaveThread     func() uintptr
	pyEvalRestoreThread  func(uintptr)
	pyGILStateEnsure     func() int32
	pyGILStateRelease    func(int32)
	pyLongFromLongLong   func(int64) uintptr
	pyLongAsLongLong     func(uintptr) int64
	pyCMethodNew         func(def, self, module, cls uintptr) uintptr
	pyImportImportModule func(string) uintptr
	pyObjectSetAttrStr   func(obj uintptr, name string, v uintptr) int32
	pyErrSetString       func(exc uintptr, msg string)
	pyErrOccurred        func() uintptr
	pyDecRef             func(uintptr)
	pyIncRef             func(uintptr)
	pyMemRawCalloc       func(n, size uintptr) uintptr
)

// goFunc is a Go implementation of a Python callable; it runs with the GIL held.
type goFunc func(args []uintptr) (uintptr, error)

var (
	funcs      []goFunc
	pyNone     uintptr
	excRuntime uintptr
	callerTIDs sync.Map
	calls      atomic.Int64
)

// dispatch is the single C-callable entry point for every Go-defined builtin.
// self is a Python int holding the index into funcs.
func dispatch(self, args uintptr, nargs int, kwnames uintptr) uintptr {
	calls.Add(1)
	callerTIDs.Store(syscall.Gettid(), true)
	f := funcs[pyLongAsLongLong(self)]
	argv := unsafe.Slice((*uintptr)(unsafe.Pointer(args)), nargs)
	res, err := f(argv)
	if err != nil {
		pyErrSetString(excRuntime, err.Error())
		return 0
	}
	return res
}

// newBuiltin wraps f as a builtin function object named name.
func newBuiltin(name string, f goFunc, trampoline uintptr) uintptr {
	const methFastcallKeywords = 0x80 | 0x02
	funcs = append(funcs, f)
	// PyMethodDef must outlive the function object; keep it (and its name) in C memory.
	cname := pyMemRawCalloc(uintptr(len(name)+1), 1)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(cname)), len(name)), name)
	def := pyMemRawCalloc(1, 32)
	d := (*[4]uintptr)(unsafe.Pointer(def))
	d[0], d[1], d[2] = cname, trampoline, methFastcallKeywords
	self := pyLongFromLongLong(int64(len(funcs) - 1))
	fn := pyCMethodNew(def, self, 0, 0)
	pyDecRef(self)
	return fn
}

// burn recurses with a large frame so the goroutine stack must grow inside a callback.
//
//go:noinline
func burn(n int) int {
	var pad [1024]byte
	pad[n%len(pad)] = byte(n)
	if n == 0 {
		return int(pad[0])
	}
	return burn(n-1) + int(pad[n%len(pad)]&1)
}

func main() {
	pyapi.Load(pyembed.SymbolAddr0())
	fmt.Println("S2 symtab entries:", len(pyapi.Symbols))
	for _, f := range []struct {
		p    any
		name string
	}{
		{&pyInitializeEx, "Py_InitializeEx"}, {&pyFinalizeEx, "Py_FinalizeEx"},
		{&pyRunSimpleString, "PyRun_SimpleString"},
		{&pyEvalSaveThread, "PyEval_SaveThread"}, {&pyEvalRestoreThread, "PyEval_RestoreThread"},
		{&pyGILStateEnsure, "PyGILState_Ensure"}, {&pyGILStateRelease, "PyGILState_Release"},
		{&pyLongFromLongLong, "PyLong_FromLongLong"}, {&pyLongAsLongLong, "PyLong_AsLongLong"},
		{&pyCMethodNew, "PyCMethod_New"}, {&pyImportImportModule, "PyImport_ImportModule"},
		{&pyObjectSetAttrStr, "PyObject_SetAttrString"}, {&pyErrSetString, "PyErr_SetString"},
		{&pyErrOccurred, "PyErr_Occurred"}, {&pyDecRef, "Py_DecRef"}, {&pyIncRef, "Py_IncRef"},
		{&pyMemRawCalloc, "PyMem_RawCalloc"},
	} {
		pyapi.Register(f.p, f.name)
	}
	pyNone = pyapi.Addr("_Py_NoneStruct") // Py_None is &_Py_NoneStruct.

	pyInitializeEx(0) // Leave signal handling to Go.
	excRuntime = pyapi.Pointer("PyExc_RuntimeError")

	trampoline := purego.NewCallback(dispatch)
	builtins := pyImportImportModule("builtins")
	add := newBuiltin("go_add", func(args []uintptr) (uintptr, error) {
		var sum int64
		for _, a := range args {
			sum += pyLongAsLongLong(a)
			if pyErrOccurred() != 0 {
				return 0, nil // Propagate the TypeError raised by PyLong_AsLongLong.
			}
		}
		return pyLongFromLongLong(sum), nil
	}, trampoline)
	fail := newBuiltin("go_fail", func([]uintptr) (uintptr, error) {
		return 0, fmt.Errorf("boom from Go")
	}, trampoline)
	none := newBuiltin("go_none", func([]uintptr) (uintptr, error) {
		pyIncRef(pyNone)
		return pyNone, nil
	}, trampoline)
	deep := newBuiltin("go_deep", func(args []uintptr) (uintptr, error) {
		return pyLongFromLongLong(int64(burn(int(pyLongAsLongLong(args[0]))))), nil
	}, trampoline)
	for name, fn := range map[string]uintptr{"go_add": add, "go_fail": fail, "go_none": none, "go_deep": deep} {
		pyObjectSetAttrStr(builtins, name, fn)
		pyDecRef(fn)
	}
	pyDecRef(builtins)

	rc := pyRunSimpleString(`
import threading
print("deep", go_deep(2000))
assert go_add(1, 2, 3) == 6
assert go_none() is None
try:
    go_fail()
except RuntimeError as e:
    assert str(e) == "boom from Go", e
else:
    raise AssertionError("no exception")
try:
    go_add(1, "x")
except TypeError as e:
    print("TypeError propagated:", e)
results = []
def worker(i):
    for _ in range(1000):
        r = go_add(i, 1)
    results.append(r)
ts = [threading.Thread(target=worker, args=(i,)) for i in range(16)]
[t.start() for t in ts]; [t.join() for t in ts]
assert sorted(results) == list(range(1, 17)), results
print("S3 python threads ok:", len(results))
`)
	if rc != 0 {
		os.Exit(1)
	}

	// Goroutines entering Python concurrently.
	ts := pyEvalSaveThread()
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			g := pyGILStateEnsure()
			if pyRunSimpleString(fmt.Sprintf("assert go_add(%d, 100) == %d", i, i+100)) != 0 {
				failures.Add(1)
			}
			pyGILStateRelease(g)
		}()
	}
	wg.Wait()
	pyEvalRestoreThread(ts)
	threads := 0
	callerTIDs.Range(func(any, any) bool { threads++; return true })
	fmt.Printf("S3 goroutines ok: failures=%d; dispatch calls=%d from %d OS threads\n", failures.Load(), calls.Load(), threads)

	if pyFinalizeEx() != 0 || failures.Load() != 0 {
		os.Exit(1)
	}
}
