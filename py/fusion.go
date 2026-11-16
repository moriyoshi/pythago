package py

// The fusion (Config.FusedBytes): Go-owned bytes objects. The allocator is
// internal/fusion; this file installs the glue's allocator hook and the gc callback, runs
// the startup check, and converts a *fusion.Bytes (From) by adopting its slot in place.

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/moriyoshi/pythago/internal/fusion"
)

// The glue's fusion functions, resolved at install (an image older than glue ABI 5 fails
// Init's ABI check first).
var (
	cBytesAdopt                                       uintptr
	cFusionCheckTry, cFusionCheckStackRef, cGCCollect uintptr
)

// fusionSupported reports whether the glue was compiled against a CPython the fusion is
// verified on (3.13 with the GIL, 3.14 free-threaded). Tests replace it to force the
// unsupported path.
var fusionSupported = func() bool { return call0(symbol("pythago_fusion_supported")) != 0 }

// checkFusion fails closed before the interpreter starts: the fusion was asked for, and
// the image's CPython is not one it is verified on.
func checkFusion(cfg Config) error {
	if cfg.FusedBytes < 0 {
		return errors.New("py: Config.FusedBytes is negative")
	}
	if cfg.FusedBytes == 0 {
		return nil
	}
	if !fusionSupported() {
		return errors.New("py: Config.FusedBytes: the native image's CPython is not one the fusion is verified on (3.13 with the GIL, 3.14 free-threaded)")
	}
	return nil
}

// installFusion sets the allocator up and installs the glue's hook on the OBJ-domain
// allocator. It runs between the core and the main phase of initialization, attached:
// tracemalloc (-X tracemalloc) starts in the main phase and its stop() restores the
// allocator it saved at start, so it must save ours (Python/tracemalloc.c). The hook stays
// for the life of the process: Python may free fused objects until it exits, Finalize
// included. Chunks go back to Go only when idle (internal/fusion).
func installFusion(cfg Config) error {
	if cfg.FusedBytes == 0 {
		return nil
	}
	for _, s := range []struct {
		p    *uintptr
		name string
	}{
		{&cBytesAdopt, "pythago_bytes_adopt"}, {&cFusionCheckTry, "pythago_fusioncheck_tryincref"},
		{&cFusionCheckStackRef, "pythago_fusioncheck_trystackref"}, {&cGCCollect, "PyGC_Collect"},
	} {
		if *s.p = symbol(s.name); *s.p == 0 {
			return fmt.Errorf("py: Config.FusedBytes: %s is missing from the native image", s.name)
		}
	}
	c, err := fusion.Install(fusion.Options{Cap: cfg.FusedBytes, Header: int(call0(symbol("pythago_bytes_header"))),
		FreeThreaded: freeThread, Bounds: unsafe.Pointer(call0(symbol("pythago_fusion_bounds")))})
	if err != nil {
		return fmt.Errorf("py: Config.FusedBytes: %w", err)
	}
	if int32(call1(symbol("pythago_fusion_install"), uintptr(c))) != 0 {
		return errors.New("py: Config.FusedBytes: the glue refused the fusion's configuration")
	}
	return nil
}

// startFusion runs after the main phase: it registers the gc callback that publishes the
// collections' epochs (until then no chunk can leave its class, the safe direction; code
// that clears gc.callbacks has the same effect), lets the allocator run collections, runs
// the startup check, and starts the sweeper (which gives idle chunks back to Go).
func startFusion() error {
	if !fusion.On() {
		return nil
	}
	fusion.Collect = func() {
		Do(func() error {
			call0(cGCCollect)
			return nil
		})
	}
	cb, err := NewFunc("pythago_fusion_gc", func(a []*Object, _ Kwargs) (any, error) {
		switch phase, _ := a[0].GoString(); phase {
		case "start": // before the collection's stop-the-world
			fusion.GCStart()
		case "stop": // after its last start-the-world
			fusion.GCStop()
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	defer cb.Close()
	g, err := result(call0(cDictNew))
	if err != nil {
		return err
	}
	defer g.Close()
	if err := g.SetItem("cb", cb); err != nil {
		return err
	}
	if err := ExecIn(fusionGCSrc, g); err != nil {
		return err
	}
	if err := fusionSelfCheck(); err != nil {
		return fmt.Errorf("py: Config.FusedBytes: startup check: %w", err)
	}
	fusion.Sweeper()
	return nil
}

// fusionGCSrc registers the gc callback cb. A forked child (os.fork, multiprocessing's
// fork and forkserver children) runs no Go: its collections must not call cb.
const fusionGCSrc = `
import gc, os
gc.callbacks.append(cb)
def drop(cb=cb):
    try:
        gc.callbacks.remove(cb)
    except ValueError:
        pass
if hasattr(os, "register_at_fork"):
    os.register_at_fork(after_in_child=drop)
`

// fusionCheckSize is the body the startup check takes a slot for.
const fusionCheckSize = 3000

// fusionSelfCheck runs CPython's own lock-free readers (glue/pythago_fusioncheck.c) on a
// fused object freed by Python, and on its slot taken again with a payload written but
// not adopted: both must refuse the incref without writing a byte of the slot. The reader
// runs on another thread with its own thread state, as a stale reader is never the thread
// that freed or adopted the object. With the GIL there are no lock-free readers: nothing to check.
func fusionSelfCheck() error {
	if !freeThread {
		return nil
	}
	b := fusion.New(fusionCheckSize)
	if b == nil {
		return errors.New("no memory for a slot")
	}
	for i := range b.Buf() {
		b.Buf()[i] = 'o'
	}
	o, err := From(b)
	if err != nil {
		return err
	}
	p := o.Ptr()
	if p != b.Addr() {
		o.Close()
		return errors.New("the object was not adopted in its slot")
	}
	o.Close() // freed by Python: a dead header
	// The reader is a goroutine's locked thread with a thread state of its own, not a
	// Python thread calling Go: Go makes a callback from a thread that runs no Go (every
	// Python thread on Windows) wait until package initialization is done, and Init may
	// run inside it (py.Main from an init function). This thread waits detached, as
	// creating the reader's thread state may stop the world.
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		errc <- enter(func() error { return fusionCheckReader(b.Slot(), p) }, 0)
	}()
	Release(func() { err = <-errc })
	return err
}

// fusionCheckReader is the startup check's reader, on a thread that neither freed nor
// adopted the object at p in slot.
func fusionCheckReader(slot []byte, p uintptr) error {
	src := new(uintptr) // a source that still holds p: the dead header alone must refuse
	*src = p
	owned := new(int32)
	read := func(stage string) error {
		before := append([]byte(nil), slot...)
		inc := int32(call3(cFusionCheckTry, uintptr(unsafe.Pointer(src)), p, uintptr(unsafe.Pointer(owned))))
		sr := int32(call3(cFusionCheckStackRef, uintptr(unsafe.Pointer(src)), p, 0))
		runtime.KeepAlive(src)
		switch {
		case *owned != 0:
			return fmt.Errorf("%s: the reader's thread owns the object", stage)
		case inc != 0 || sr != 0:
			return fmt.Errorf("%s: a stale reader's incref succeeded (_Py_TryIncrefCompare %d, _Py_TryIncrefCompareStackRef %d)", stage, inc, sr)
		case !bytes.Equal(before, slot):
			for i := range slot {
				if before[i] != slot[i] {
					return fmt.Errorf("%s: a stale reader wrote the slot at offset %d", stage, i)
				}
			}
		}
		return nil
	}
	if err := read("a freed object"); err != nil {
		return err
	}
	b := fusion.New(fusionCheckSize) // the same class: the freed slot comes back first
	if b == nil || b.Addr() != p {
		b.Release()
		return errors.New("the freed slot was not taken again")
	}
	defer b.Release()
	for i := range b.Buf() {
		b.Buf()[i] = 0xa5
	}
	return read("a retaken slot")
}

// fromFused converts a *fusion.Bytes: its slot becomes the bytes object, in place. A
// body under 2 bytes becomes Python's singleton, and in a subinterpreter (whose objects
// and collections are its own) the body is copied; the slot then goes back.
func fromFused(b *fusion.Bytes) (uintptr, error) {
	slot, n, ok := b.Claim()
	if !ok {
		return 0, errors.New("py: a fused body converted twice, or after its release")
	}
	if n >= 2 {
		if r := call2(cBytesAdopt, uintptr(slot), uintptr(n)); r != 0 {
			return r, nil
		}
	}
	data := b.Data()
	r := call2(cBytesFromStringAndSize, uintptr(unsafe.Pointer(unsafe.SliceData(data))), uintptr(len(data)))
	runtime.KeepAlive(data)
	b.Abandon()
	return rawResult(r)
}
