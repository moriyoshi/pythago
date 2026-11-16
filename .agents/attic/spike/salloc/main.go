// Command salloc backs pymalloc's arenas with Go heap memory through
// PyObject_SetArenaAllocator, so Python object memory is visible to the Go GC
// pacer and GOMEMLIMIT. Only arena refills (1 MiB, under the GIL) cross into Go;
// pymalloc's small-object fast path stays native.
//
//	PYTHONHOME=<pbs install> salloc
package main

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/moriyoshi/pythago/internal/spike/pyapi"
	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

func init() { runtime.LockOSThread() }

var (
	arenaMu   sync.Mutex
	arenas    = map[uintptr][]byte{} // Keeps arenas reachable; the Go heap does not move.
	allocs    int
	frees     int
	peakBytes int
	liveBytes int
)

func arenaAlloc(_ uintptr, size uintptr) uintptr {
	b := make([]byte, size)
	p := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	arenaMu.Lock()
	arenas[p] = b
	allocs++
	liveBytes += int(size)
	peakBytes = max(peakBytes, liveBytes)
	arenaMu.Unlock()
	return p
}

func arenaFree(_ uintptr, p uintptr, size uintptr) {
	arenaMu.Lock()
	if _, ok := arenas[p]; !ok {
		panic(fmt.Sprintf("arena free of unknown %#x", p))
	}
	delete(arenas, p)
	frees++
	liveBytes -= int(size)
	arenaMu.Unlock()
}

func heapMiB() float64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapAlloc) / (1 << 20)
}

func main() {
	var (
		initialize   func(int32)
		run          func(string) int32
		finalize     func() int32
		setArenaFunc func(uintptr)
	)
	pyapi.Load(pyembed.SymbolAddr0())
	pyapi.Register(&initialize, "Py_InitializeEx")
	pyapi.Register(&run, "PyRun_SimpleString")
	pyapi.Register(&finalize, "Py_FinalizeEx")
	pyapi.Register(&setArenaFunc, "PyObject_SetArenaAllocator")

	// PyObjectArenaAllocator {ctx, alloc, free}; pymalloc copies it.
	allocator := [3]uintptr{0, purego.NewCallback(arenaAlloc), purego.NewCallback(arenaFree)}
	if os.Getenv("SALLOC_OFF") == "" {
		setArenaFunc(uintptr(unsafe.Pointer(&allocator)))
	}

	initialize(0)
	fmt.Printf("after init: %d arenas, Go heap %.1f MiB\n", len(arenas), heapMiB())
	rc := run(`
import sys, threading, gc
blob = [(i, str(i), [i]) for i in range(2_000_000)]
print("allocated, pymalloc arenas:", sys._debugmallocstats is not None)
def churn():
    for _ in range(20):
        x = [{"k": i} for i in range(50_000)]
ts = [threading.Thread(target=churn) for _ in range(8)]
[t.start() for t in ts]; [t.join() for t in ts]
`)
	fmt.Printf("with 2M tuples live: %d arenas (%d MiB), Go heap %.1f MiB\n", len(arenas), liveBytes>>20, heapMiB())
	rc |= run("del blob; import gc; gc.collect()")
	runtime.GC()
	fmt.Printf("after del: %d arenas (%d MiB), Go heap %.1f MiB\n", len(arenas), liveBytes>>20, heapMiB())
	if finalize() != 0 {
		fmt.Fprintln(os.Stderr, "Py_FinalizeEx failed")
	}
	fmt.Printf("after finalize: allocs %d, frees %d, peak %d MiB, live %d\n", allocs, frees, peakBytes>>20, len(arenas))
	os.Exit(int(rc))
}
