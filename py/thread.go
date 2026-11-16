package py

import (
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	closing  = make(chan struct{}) // closed by Finalize
	inflight sync.RWMutex          // read-held by every outer Do; Finalize takes it to wait them out
)

// Do runs fn with a Python thread state attached, on the calling goroutine locked to its
// current OS thread. Calls nest: inside Do, inside a Go function called from Python, or
// inside Release, Do just runs fn on the same thread state.
//
// Outside Python, Do first waits for an admission slot ([Config.Slots]). Work that fn
// hands to another goroutine which itself calls Do needs a slot of its own, so waiting
// for such work inside fn can deadlock when slots run out; do it on the same goroutine,
// or wait inside [Release], which gives the slot back.
func Do(fn func() error) error {
	if int32(call0(cPyDepth)) > 0 {
		// Inside Python on this OS thread already, which means this goroutine is the one
		// locked to it: no other goroutine runs on a locked thread.
		return enter(fn, 0)
	}
	if state.Load() != stateRunning {
		return ErrNotRunning
	}
	inflight.RLock()
	defer inflight.RUnlock()
	if state.Load() != stateRunning {
		return ErrNotRunning
	}
	select {
	case slots <- struct{}{}:
	case <-closing:
		return ErrNotRunning
	}
	defer putSlot()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return enter(fn, 1)
}

func enter(fn func() error, slot uintptr) error {
	attached := int32(call1(cPyEnter, slot))
	if attached < 0 {
		return ErrNoThreadState
	}
	defer call2(cPyLeave, uintptr(attached), slot)
	if pendingN.Load() > 0 || goBuffersReleased() {
		drainPending()
	}
	return fn()
}

// Release detaches the calling goroutine's Python thread state while fn runs, so that
// blocking Go code inside Do, or inside a Go function called from Python, does not hold
// the GIL or an admission slot, or hold up free-threaded stop-the-world pauses. fn must
// not use Python objects except inside a nested Do.
//
// A Do that gave its admission slot back in Release gets one again ahead of goroutines
// waiting to enter Do, so that a request already under way, which Release'd to write or
// read, is not queued behind new ones. While both wait, goroutines entering Do still get
// at least one of every five slots that come free.
func Release(fn func()) {
	var slot int32
	ts := call1(cPyRelease, uintptr(unsafe.Pointer(&slot)))
	if slot != 0 {
		putSlot()
	}
	defer func() {
		if slot != 0 {
			getSlotBack() // Finalize waits for this Do (inflight), so no select on closing
		}
		call2(cPyReacquire, ts, uintptr(slot))
	}()
	fn()
}

// Admission slots. slots is a semaphore: a goroutine holds a slot while its token is in
// the channel. Do takes one by sending (FIFO among the goroutines entering Do, with
// Finalize as the way out); every slot is given back through putSlot, which hands it
// straight to a goroutine waiting in getSlotBack (returning from Release) if there is
// one: the token then stays in the channel and changes owner.
var admission admissionState

type admissionState struct {
	mu        sync.Mutex
	returning atomic.Int32    // goroutines in getSlotBack: queued, or about to try the channel
	queue     []chan struct{} // the queued ones, FIFO; closing one hands it a slot
	run       int             // slots handed over in a row since the queue was last empty; see returnRun
}

// returnRun is how many slots in a row putSlot hands to goroutines returning from
// Release before it frees one in the channel, which goes to a goroutine waiting in Do
// if there is one, so that a stream of returns does not shut new requests out.
const returnRun = 4

// putSlotWindow, if set (by tests), runs in putSlot between its check for returns and
// its receive.
var putSlotWindow func()

// putSlot gives back the caller's admission slot.
func putSlot() {
	a := &admission
	if a.returning.Load() == 0 {
		if putSlotWindow != nil {
			putSlotWindow()
		}
		<-slots
		if a.returning.Load() != 0 {
			// A return queued between the load and the receive, finding no room: the slot
			// just freed is its, unless Do took it first (it gives it back here later).
			a.mu.Lock()
			a.fill()
			a.mu.Unlock()
		}
		return
	}
	a.mu.Lock()
	if len(a.queue) > 0 && a.run < returnRun {
		a.run++
		a.handFirst()
	} else {
		a.run = 0
		<-slots  // a goroutine blocked in Do's send takes the slot at once (FIFO)
		a.fill() // nobody did: the queued returns take it
	}
	a.mu.Unlock()
}

// getSlotBack takes an admission slot for a Do that gave its own back in Release.
func getSlotBack() {
	a := &admission
	a.mu.Lock()
	a.returning.Add(1)
	select {
	case slots <- struct{}{}:
		a.returning.Add(-1)
		a.mu.Unlock()
		return
	default:
	}
	// Every slot is held, and each holder gives its slot back through putSlot, which
	// sees this one (returning is set before the channel is tried).
	ch := make(chan struct{})
	if len(a.queue) == 0 {
		a.run = 0 // the returns' turn comes round again
	}
	a.queue = append(a.queue, ch)
	a.mu.Unlock()
	<-ch
}

// fill hands free slots to queued returns. Locked.
func (a *admissionState) fill() {
	for len(a.queue) > 0 {
		select {
		case slots <- struct{}{}:
			a.handFirst()
		default:
			return
		}
	}
}

// handFirst hands the first queued return the slot the caller holds. Locked.
func (a *admissionState) handFirst() {
	ch := a.queue[0]
	a.queue[0] = nil
	a.queue = a.queue[1:]
	if len(a.queue) == 0 {
		a.queue = nil // let the array go, rather than creep along it
	}
	a.returning.Add(-1)
	close(ch)
}

// threadStart is the Go spawn hook of pythago_py_spawn: it starts a goroutine, locked to
// its own OS thread, that runs the Python thread record t (or, for t == 0, waits in C as a
// spare until pthread_create hands it one). The handshake, the thread cap, join and detach
// are all in C.
func threadStart(t uintptr) uintptr {
	go runThread(t, make(chan struct{}))
	return 0
}

// runThread runs t on an OS thread that never ran Python, and keeps it locked: the thread
// exits with the goroutine after the Python thread was joined or detached, running TLS
// destructors as a libc-created thread would.
//
// An OS thread that ran Python outside a Python thread (Do) must outlive the program's
// use of it, as the threads running a program's own code do under CPython: native code may
// keep the identity of the thread it was loaded or first used on. mimalloc (in Rust
// extensions such as pyreqwest) takes that thread as its main thread, by thread pointer;
// when it exits, a new thread on its cached stack gets the same pointer and mimalloc's
// main heap, which then two threads use ("addr=0x18" crashes; journal 2026-11-21). A
// Python thread's own OS thread exits at its end, as a libc-created thread does, so a
// Python thread never runs on a thread that ran Do: Go may start the goroutine on such an
// idle thread, which it then holds, so that Go starts the next goroutine on another, and
// lets go once a thread that never ran Python takes t (found).
//
// Reusing a Python thread's OS thread is not known to be unsafe: the TestMixedThreads
// crashes once blamed on it (free-threaded, fusion on, qemu-x86_64) were the glue storing
// a thread state under the native image's TLS key (pythago_py_enter, fixed), and with
// that fixed the reusing design passes too.
func runThread(t uintptr, found chan struct{}) {
	runtime.LockOSThread()
	if call0(cGILStateGetThisThreadState) != 0 { // Do left its thread state bound here
		go runThread(t, found)
		<-found
		runtime.UnlockOSThread()
		return
	}
	close(found)
	call1(cPyThreadMain, t)
}

// SetThreadLimit sets the cap on live Python threads and returns the previous one. It
// has no effect on Windows, where CPython starts its own threads.
func SetThreadLimit(n int) int { return int(int64(call1(cPySetLimit, uintptr(n)))) }

// Stats reports thread counters.
type Stats struct {
	Threads             int64 // live goroutine-backed Python threads
	ThreadsStarted      int64 // goroutine-backed Python threads started so far
	ThreadStatesCreated int64 // thread states Do created for OS threads that had none
	ThreadStatesDeleted int64 // of those, deleted when their OS thread ran Python or exited
	GoBuffers           int64 // Go slices pinned for Python objects from NewBuffer
}

// ReadStats returns the current counters.
func ReadStats() Stats {
	var c [4]int64
	call1(cPyStats, uintptr(unsafe.Pointer(&c[0])))
	return Stats{
		Threads:             c[2],
		ThreadsStarted:      c[3],
		ThreadStatesCreated: c[0],
		ThreadStatesDeleted: c[1],
		GoBuffers:           liveGoBuffers(),
	}
}
