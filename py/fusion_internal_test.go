package py

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
	"weak"

	"github.com/moriyoshi/pythago/internal/fusion"
)

// The fusion's tests (Config.FusedBytes). Most need it on: PYTHAGO_TEST_FUSION=1 (the
// tests' TestMain); TestFusedBytesOff needs it off. Both ci/unit.sh passes run them all.

func fusionOn(t *testing.T) {
	t.Helper()
	if !fusion.On() {
		t.Skip("PYTHAGO_TEST_FUSION=1 runs the fusion tests")
	}
}

// fusionFind is the glue hook's lookup of p: its cell, or -1.
func fusionFind(p uintptr) int {
	return int(int64(call1(symbol("pythago_fusion_find"), p)))
}

// cellOf is the chunk cell holding p, as Go knows it (-1: none).
func cellOf(p uintptr) int {
	id, _, ok := fusion.Locate(p)
	if !ok {
		return -1
	}
	return id
}

func chunkLive(id int) int64 { return fusion.Chunk(id).Live }

// fused makes a fused bytes object of data (a new Object).
func fused(t *testing.T, data []byte) *Object {
	t.Helper()
	fb := fusion.New(len(data))
	if fb == nil { // every chunk held, some waiting for a grace (an earlier test's): one
		gcCollect(t)
		fb = fusion.New(len(data))
	}
	if fb == nil {
		t.Fatalf("no fused memory for %d bytes", len(data))
	}
	copy(fb.Buf(), data)
	fb.Truncate(len(data))
	o, err := From(fb)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func gcDisable(t *testing.T) func() {
	t.Helper()
	r, err := Eval("__import__('gc').disable()")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	return func() { r, _ := Eval("__import__('gc').enable()"); r.Close() }
}

func gcCollect(t *testing.T) {
	t.Helper()
	r, err := Eval("__import__('gc').collect()")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

// A fused bytes object behaves as the same bytes made by PyBytes_FromStringAndSize, in
// every way Python code can observe; it is in its slot, which the hook finds.
func TestFusedBytesBehaviour(t *testing.T) {
	fusionOn(t)
	if err := Do(func() error {
		g, err := Eval("{}")
		if err != nil {
			return err
		}
		defer g.Close()
		if err := ExecIn(`
import pickle, marshal, sys, gc, copy
def compare(f, s):
    bad = []
    def check(name, a, b):
        if a != b:
            bad.append("%s: %r != %r" % (name, a, b))
    check("type", type(f) is bytes, type(s) is bytes)
    check("eq", f == s, True)
    check("hash", hash(f), hash(s))
    check("len", len(f), len(s))
    check("getsizeof", sys.getsizeof(f), sys.getsizeof(s))
    check("pickle", pickle.loads(pickle.dumps(f)), s)
    check("marshal", marshal.loads(marshal.dumps(f)), s)
    check("slice", f[1:-1], s[1:-1])
    check("bytes() identity", bytes(f) is f, bytes(s) is s)
    check("copy identity", copy.copy(f) is f, copy.copy(s) is s)
    check("memoryview", memoryview(f).tobytes(), s)
    check("readonly", memoryview(f).readonly, True)
    check("decode", f.decode("latin-1"), s.decode("latin-1"))
    check("concat", f + b"x", s + b"x")
    check("tracked", gc.is_tracked(f), gc.is_tracked(s))
    check("dict key", {f: 1}[s], 1)
    check("repr", repr(f), repr(s))
    check("find", f.find(s[-2:]), s.find(s[-2:]))
    check("nul", f + b"" == s, True)
    return bad
`, g); err != nil {
			return err
		}
		compare, err := g.Item("compare")
		if err != nil {
			return err
		}
		defer compare.Close()
		for _, n := range []int{2, 7, 16, 17, 100, 129, 4096, 5000, 16384, 60000, 65536} {
			data := make([]byte, n)
			for i := range data {
				data[i] = byte(i*7 + n)
			}
			f := fused(t, data)
			if id := cellOf(f.Ptr()); id < 0 || fusionFind(f.Ptr()) != id {
				t.Fatalf("%d bytes: the object is not in a chunk the hook finds (cell %d, hook %d)", n, id, fusionFind(f.Ptr()))
			}
			s, _ := From(data)
			r, err := compare.Call(f, s)
			if err != nil {
				return err
			}
			v, _ := r.Go()
			r.Close()
			if bad := v.([]any); len(bad) > 0 {
				t.Errorf("%d bytes: %v", n, bad)
			}
			f.Close()
			s.Close()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Bodies shorter than 2 bytes become Python's singletons, as PyBytes_FromStringAndSize
// gives, and their slot goes back at once.
func TestFusedBytesSingletons(t *testing.T) {
	fusionOn(t)
	Do(func() error {
		for _, data := range [][]byte{{}, {'x'}} {
			fb := fusion.New(2)
			live := chunkLive(fb.Cell())
			copy(fb.Buf(), data)
			fb.Truncate(len(data))
			o, err := From(fb)
			if err != nil {
				t.Fatal(err)
			}
			s, _ := From(data)
			if o.Ptr() != s.Ptr() {
				t.Errorf("%q: not the singleton", data)
			}
			if got := chunkLive(fb.Cell()); got != live-1 {
				t.Errorf("%q: the chunk's live count is %d, want %d", data, got, live-1)
			}
			o.Close()
			s.Close()
		}
		return nil
	})
}

// A *fusion.Bytes converts once: a second conversion, or one after Release, fails.
func TestFusedBytesConvertOnce(t *testing.T) {
	fusionOn(t)
	Do(func() error {
		fb := fusion.New(100)
		fb.Truncate(100)
		o, err := From(fb)
		if err != nil {
			t.Fatal(err)
		}
		o.Close()
		if _, err := From(fb); err == nil {
			t.Error("a second conversion succeeded")
		}
		fb = fusion.New(100)
		fb.Release()
		if _, err := From(fb); err == nil {
			t.Error("a conversion after Release succeeded")
		}
		return nil
	})
}

// _PyBytes_Resize of a uniquely referenced fused object moves it out of its chunk
// (PyObject_Realloc through the hook) with its contents, and frees its slot.
func TestFusedBytesResize(t *testing.T) {
	fusionOn(t)
	Do(func() error {
		data := []byte("0123456789abcdefghij")
		o := fused(t, data)
		p := o.steal()
		id := cellOf(p)
		live := chunkLive(id)
		if rc := call2(symbol("_PyBytes_Resize"), uintptr(unsafe.Pointer(&p)), 10); int32(rc) != 0 {
			t.Fatal(fetchError())
		}
		r := own(p)
		if cellOf(p) >= 0 {
			t.Errorf("the resized object is still in a chunk")
		}
		if b, _ := r.Bytes(); string(b) != "0123456789" {
			t.Errorf("resized to %q", b)
		}
		if got := chunkLive(id); got != live-1 {
			t.Errorf("the chunk's live count is %d after the move, want %d", got, live-1)
		}
		r.Close()
		return nil
	})
}

// A slot Python freed is taken again at once, within its class, with no collection
// between (both builds), and Go's take and payload write leave the bytes before ob_type
// as Python left them; only adoption rewrites them.
func TestFusedBytesSameClassReuse(t *testing.T) {
	fusionOn(t)
	defer fusion.PauseSweeper(fusion.PauseSweeper(true)) // its sweeps would change the counts
	typeOff := int(call0(symbol("pythago_bytes_typeoff")))
	Do(func() error {
		defer gcDisable(t)()
		collections := fusion.ReadStats().Collections
		o := fused(t, bytes.Repeat([]byte{'a'}, 3000))
		p := o.Ptr()
		o.Close() // freed by Python
		id, off, _ := fusion.Locate(p)
		mem := fusion.Chunk(id).Mem
		dead := append([]byte(nil), mem[off:off+typeOff]...)
		fb := fusion.New(2900) // the same class
		if fb == nil || fb.Addr() != p {
			t.Fatalf("the freed slot was not taken again at once")
		}
		for i := range fb.Buf() {
			fb.Buf()[i] = 0xa5
		}
		if now := mem[off : off+typeOff]; !bytes.Equal(now, dead) {
			t.Errorf("taking the slot and writing its payload changed its header: % x, was % x", now, dead)
		}
		fb.Truncate(2900)
		o2, err := From(fb)
		if err != nil {
			t.Fatal(err)
		}
		if o2.Ptr() != p {
			t.Errorf("adopted elsewhere")
		}
		if b, _ := o2.Bytes(); len(b) != 2900 || b[0] != 0xa5 || b[2899] != 0xa5 {
			t.Errorf("the reused slot's object holds %d bytes", len(b))
		}
		o2.Close()
		if fusion.ReadStats().Collections != collections {
			t.Errorf("a collection ran")
		}
		return nil
	})
}

// stalePy holds on_thread(fn, *args): fn(*args) called on a new Python thread.
const stalePy = `
import threading
def on_thread(fn, *a):
    r = []
    t = threading.Thread(target=lambda: r.append(fn(*a)))
    t.start()
    t.join()
    return r[0]
def hold_on_thread(fn, obj, *a):
    # obj is passed on the new thread: a reference of a thread that does not own it
    # (ob_ref_shared), held during fn.
    return on_thread(lambda *b: fn(obj, *b), *a)
`

// TestFusedBytesStaleReader is the deterministic stale-reader test (free-threaded): a
// lock-free reader that loaded a fused object's pointer from a list before the object
// was freed runs its incref later, at each point of the slot's next life, with CPython's
// real readers (_Py_TryIncrefCompare and _Py_TryIncrefCompareStackRef, compiled from its
// internal headers in glue/pythago_fusioncheck.c), on a thread that neither freed nor
// adopted the object (a stale reader never is that thread):
//
//	(a) the slot is free (dead header);
//	(b) Go took the slot again and adopted a new object there: owned by another thread
//	    (ob_ref_shared 0), and also referenced by the reader's thread (ob_ref_shared > 0,
//	    so the reader's CAS and decref run);
//	(c) Go took the slot again and wrote the payload, not yet adopted.
//
// The reader runs with the real list slot as its source (which no longer holds p) and,
// in (a) and (c), with a source that still holds p: the dead header alone must refuse the
// incref. No byte of the chunk may change across the reader's calls, and in (a) and (c)
// the reader must fail.
func TestFusedBytesStaleReader(t *testing.T) {
	fusionOn(t)
	defer fusion.PauseSweeper(fusion.PauseSweeper(true)) // its sweeps would change the counts
	if !freeThread {
		t.Skip("lock-free readers exist only on free-threaded builds")
	}
	var staleChunk []byte
	cItem := symbol("pythago_fusioncheck_listitem")
	cell := new(uintptr) // a source that still holds p
	probe := func(a []*Object, _ Kwargs) (any, error) {
		// (src kind, list, p): runs the readers on this thread; returns their results,
		// whether this thread owns p, ob_ref_shared before, and the first chunk byte that
		// changed across the calls (-1: none) with 8 bytes from there before and after.
		kind, _ := a[0].Int64()
		pi, _ := a[2].Int64()
		p := uintptr(pi)
		src := uintptr(unsafe.Pointer(cell))
		if kind == 0 {
			src = call2(cItem, a[1].Ptr(), 0)
		} else {
			*cell = p
		}
		_, off, _ := fusion.Locate(p)
		shared := int64(0)
		for i := range 8 {
			shared |= int64(staleChunk[off+16+i]) << (8 * i)
		}
		before := append([]byte(nil), staleChunk...)
		var owned int32
		tryInc := int32(call3(cFusionCheckTry, src, p, uintptr(unsafe.Pointer(&owned))))
		trySR := int32(call3(cFusionCheckStackRef, src, p, 1))
		at, was, now := int64(-1), "", ""
		for i := range staleChunk {
			if before[i] != staleChunk[i] {
				at = int64(i)
				was = fmt.Sprintf("% x", before[i:min(i+8, len(before))])
				now = fmt.Sprintf("% x", staleChunk[i:min(i+8, len(before))])
				copy(staleChunk, before) // the next stage starts from the same state
				break
			}
		}
		return []any{int64(tryInc), int64(trySR), int64(owned), shared, at, was, now}, nil
	}
	probeHeld := func(a []*Object, kw Kwargs) (any, error) { return probe(a[1:], kw) }
	var g *Object
	Do(func() error {
		g = dispatchGlobals(t, stalePy, map[string]Func{"probe": probe, "probe_held": probeHeld})
		return nil
	})
	defer Do(func() error { g.Close(); return nil })

	Do(func() error {
		defer gcDisable(t)()
		onThread, _ := g.Item("on_thread")
		holdOnThread, _ := g.Item("hold_on_thread")
		probeFn, _ := g.Item("probe")
		probeHeldFn, _ := g.Item("probe_held")
		defer onThread.Close()
		defer holdOnThread.Close()
		defer probeFn.Close()
		defer probeHeldFn.Close()

		const n = 3000
		o := fused(t, bytes.Repeat([]byte{'o'}, n))
		p := o.Ptr()
		lst, _ := Eval("[None]")
		defer lst.Close()
		if err := lst.SetItem(0, o); err != nil {
			t.Fatal(err)
		}
		o.Close() // the list holds the only reference
		id, slot, _ := fusion.Locate(p)
		staleChunk = fusion.Chunk(id).Mem
		none, _ := Eval("None")
		if err := lst.SetItem(0, none); err != nil { // the list drops it: freed
			t.Fatal(err)
		}
		none.Close()

		// check runs the readers on another thread with each source.
		check := func(stage string, srcs []int64, wantFail bool, hold *Object) {
			t.Helper()
			for _, kind := range srcs {
				var r *Object
				var err error
				if hold != nil {
					r, err = holdOnThread.Call(probeHeldFn, hold, kind, lst, int64(p))
				} else {
					r, err = onThread.Call(probeFn, kind, lst, int64(p))
				}
				if err != nil {
					t.Fatal(err)
				}
				v, _ := r.Go()
				r.Close()
				res := v.([]any)
				tryInc, trySR, owned, shared, at := res[0].(int64), res[1].(int64), res[2].(int64), res[3].(int64), res[4].(int64)
				src := map[int64]string{0: "the list slot", 1: "a source still holding p"}[kind]
				t.Logf("%s, %s: _Py_TryIncrefCompare %d, _Py_TryIncrefCompareStackRef %d, ob_ref_shared before %#x, changed at %d",
					stage, src, tryInc, trySR, shared, at)
				if owned != 0 {
					t.Fatalf("%s: the reader thread owns the object (setup)", stage)
				}
				if hold != nil && shared <= 0 {
					t.Fatalf("%s: ob_ref_shared is %d: the reader's CAS would not run (setup)", stage, shared)
				}
				if wantFail && (tryInc != 0 || trySR != 0) {
					t.Errorf("%s, %s: the stale reader's incref succeeded (_Py_TryIncrefCompare %d, _Py_TryIncrefCompareStackRef %d)", stage, src, tryInc, trySR)
				}
				if tryInc != 0 && kind == 0 {
					t.Errorf("%s, %s: _Py_TryIncrefCompare returned %d for a pointer the list no longer holds", stage, src, tryInc)
				}
				if at >= 0 {
					t.Errorf("%s, %s: the stale reader changed the chunk at slot offset %+d (%s -> %s)", stage, src, at-int64(slot), res[5], res[6])
				}
			}
		}

		// (a) the slot is free: its dead header.
		check("(a) freed", []int64{0, 1}, true, nil)

		// (b) Go takes the slot again (the chunk's free stack is LIFO) and adopts a new object.
		fb := fusion.New(n)
		if fb == nil || fb.Addr() != p {
			t.Fatalf("(b): the freed slot was not taken again (setup)")
		}
		copy(fb.Buf(), bytes.Repeat([]byte{'n'}, n))
		o2, err := From(fb)
		if err != nil || o2.Ptr() != p {
			t.Fatalf("(b): adopted elsewhere (setup)")
		}
		check("(b) a new object owned by another thread", []int64{0}, true, nil)
		check("(b) a new object also referenced by the reader's thread", []int64{0}, false, o2)
		if b, _ := o2.Bytes(); !bytes.Equal(b, bytes.Repeat([]byte{'n'}, n)) {
			t.Errorf("(b): the new object's payload changed")
		}
		o2.Close() // freed by Python again

		// (c) Go takes the slot and writes the payload; not adopted yet.
		fb = fusion.New(n)
		if fb == nil || fb.Addr() != p {
			t.Fatalf("(c): the freed slot was not taken again (setup)")
		}
		for i := range fb.Buf() {
			fb.Buf()[i] = 0xa5
		}
		check("(c) taken, payload written", []int64{0, 1}, true, nil)
		fb.Release()
		return nil
	})
}

// fillChunk takes slots of size bytes until it gets one from a chunk new to the class (its
// first slot carved), then fills that chunk with fused objects. It returns the chunk's
// cell and its objects, and the other objects taken on the way.
func fillChunk(t *testing.T, size int) (int, []*Object, []*Object) {
	t.Helper()
	var objs, other []*Object
	victim := -1
	for victim < 0 {
		o := fused(t, bytes.Repeat([]byte{'v'}, size))
		id := cellOf(o.Ptr())
		if c := fusion.Chunk(id); c.Carved == 1 && !c.Free && c.Live == 1 {
			victim = id
			objs = append(objs, o)
		} else {
			other = append(other, o)
		}
		if len(other) > 4096 {
			t.Fatal("no fresh chunk (setup)")
		}
	}
	for fusion.Chunk(victim).Carved < fusion.Chunk(victim).Slots {
		o := fused(t, bytes.Repeat([]byte{'v'}, size))
		if cellOf(o.Ptr()) != victim {
			t.Fatal("a slot of another chunk while the victim had room (setup)")
		}
		objs = append(objs, o)
	}
	return victim, objs, other
}

// A chunk whose slots Python freed leaves its class (here: is taken by another class,
// with every chunk held) only after a stop-the-world began after its last free
// (free-threaded); with the GIL, at once. No slot of the other class may land in it
// before gc.collect(), and one does after. If one lands before, a stale reader of one of
// the freed objects (an object header in the new class's payload) is run, to show what
// it does.
func TestFusedBytesClassChangeGrace(t *testing.T) {
	fusionOn(t)
	defer fusion.PauseSweeper(fusion.PauseSweeper(true)) // its sweeps would change the counts
	Do(func() error {
		defer gcDisable(t)()
		defer fusion.SetNoCollect(fusion.SetNoCollect(true))
		const sizeA = 1000
		victim, objs, other := fillChunk(t, sizeA)
		defer func() {
			for _, o := range other {
				o.Close()
			}
		}()
		base := fusion.Chunk(victim).Base
		var oldHeader uintptr
		for _, o := range objs {
			if oldHeader == 0 && o.Ptr() > base+512 {
				oldHeader = o.Ptr() // a header some way into the chunk
			}
			o.Close() // freed by Python: stamped
		}
		if chunkLive(victim) != 0 {
			t.Fatalf("the victim chunk has live slots (setup)")
		}
		// Take every chunk with class B (64 KiB bodies), held, not adopted.
		var held []*fusion.Bytes
		defer func() {
			for _, fb := range held {
				fb.Release()
			}
		}()
		landed := false
		for {
			fb := fusion.New(fusion.MaxCap)
			if fb == nil {
				break
			}
			held = append(held, fb)
			if fb.Cell() == victim {
				landed = true
				break
			}
		}
		if freeThread && landed {
			t.Errorf("a chunk whose objects Python freed changed class before any collection")
			// What a stale reader of a freed class-A object would do now: the class-B
			// slot's payload covers the old header.
			fb := held[len(held)-1]
			for i := range fb.Buf() {
				fb.Buf()[i] = 0xa5
			}
			id, off, _ := fusion.Locate(oldHeader)
			mem := fusion.Chunk(id).Mem
			cell := new(uintptr)
			*cell = oldHeader
			owned := new(int32)
			before := fmt.Sprintf("% x", mem[off:off+24])
			res := int32(call3(cFusionCheckTry, uintptr(unsafe.Pointer(cell)), oldHeader, uintptr(unsafe.Pointer(owned))))
			t.Errorf("a stale reader of a freed object in it now: _Py_TryIncrefCompare %d, the old header's bytes (class B payload) %s -> % x", res, before, mem[off:off+24])
		}
		if !freeThread && !landed {
			t.Errorf("GIL: the freed chunk was not reused for another class at once")
		}
		if freeThread && !landed {
			if fusion.Chunk(victim).Base != base || fusion.Chunk(victim).Class != fusion.ClassFor(sizeA) {
				t.Fatalf("the chunk left its class before any collection")
			}
			gcCollect(t)
			for !landed { // other chunks may be past their grace now too
				fb := fusion.New(fusion.MaxCap)
				if fb == nil {
					break
				}
				held = append(held, fb)
				landed = fb.Cell() == victim
			}
			if !landed {
				t.Errorf("after a collection, the freed chunk was not reused")
			}
		}
		return nil
	})
}

// With every chunk held and one waiting only for a grace, the allocator runs a collection
// in the background; the chunk is then reused (free-threaded).
func TestFusedBytesCollectFallback(t *testing.T) {
	fusionOn(t)
	defer fusion.PauseSweeper(fusion.PauseSweeper(true)) // its sweeps would change the counts
	if !freeThread {
		t.Skip("no grace with the GIL")
	}
	var held []*fusion.Bytes
	defer func() {
		for _, fb := range held {
			fb.Release()
		}
	}()
	var victim int
	Do(func() error {
		var objs, other []*Object
		victim, objs, other = fillChunk(t, 300)
		for _, o := range append(objs, other...) {
			o.Close()
		}
		return nil
	})
	calls := fusion.ReadStats().CollectCalls
	for {
		fb := fusion.New(fusion.MaxCap)
		if fb == nil {
			break
		}
		held = append(held, fb)
	}
	if chunkLive(victim) != 0 {
		t.Skipf("the chunk has live slots (another test's)")
	}
	deadline := time.Now().Add(10 * time.Second)
	var fb *fusion.Bytes
	for time.Now().Before(deadline) {
		if fb = fusion.New(fusion.MaxCap); fb != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fb == nil {
		t.Errorf("the cap stayed exhausted; collections run: %d", fusion.ReadStats().CollectCalls-calls)
	} else {
		held = append(held, fb)
		if fusion.ReadStats().CollectCalls == calls {
			t.Errorf("memory came back without a collection")
		}
	}
}

// A chunk goes back to Go (unregistered, unpinned, dropped) once it is empty and past its
// grace, after the idle decay's sweeps (the sweeper's, here run by hand), and not before:
// not while a slot is live, and on free-threaded builds not without a collection after its
// last free. The hook then no longer claims its addresses.
func TestFusedBytesRelease(t *testing.T) {
	fusionOn(t)
	defer fusion.PauseSweeper(fusion.PauseSweeper(true)) // its sweeps would change the counts
	var wp weak.Pointer[byte]
	Do(func() error {
		defer gcDisable(t)()
		victim, objs, other := fillChunk(t, 40000)
		for _, o := range other {
			o.Close()
		}
		c := fusion.Chunk(victim)
		base := c.Base
		wp = weak.Make(&c.Mem[0])
		c = fusion.ChunkInfo{}
		for _, o := range objs[1:] {
			o.Close()
		}
		// One live slot: never released, collections or not.
		for range fusion.DemoteSweeps + fusion.IdleSweeps + fusion.IdleKeep + 2 {
			gcCollect(t)
			fusion.Sweep()
		}
		if c := fusion.Chunk(victim); !c.Held || c.Base != base || c.Class < 0 || fusionFind(base) != victim {
			t.Fatalf("a chunk with a live slot left its class (held %v, class %d)", c.Held, c.Class)
		}
		objs[0].Close() // live 0, stamped
		if freeThread {
			for range fusion.DemoteSweeps + fusion.IdleSweeps + 2 {
				fusion.Sweep() // no grace published
			}
			if c := fusion.Chunk(victim); !c.Held || c.Base != base || c.Class < 0 || fusionFind(base) != victim {
				t.Fatalf("a chunk left its class with no collection after its last free (held %v, class %d)", c.Held, c.Class)
			}
		}
		gcCollect(t) // the grace
		sweeps := 0
		for ; sweeps < fusion.DemoteSweeps+fusion.IdleSweeps+fusion.IdleKeep+4; sweeps++ {
			if c := fusion.Chunk(victim); !c.Held || c.Base != base {
				break
			}
			fusion.Sweep()
		}
		if c := fusion.Chunk(victim); c.Held && c.Base == base {
			t.Fatalf("not released after %d sweeps past its grace", sweeps)
		}
		t.Logf("released after %d sweeps", sweeps)
		if sweeps < fusion.DemoteSweeps {
			t.Errorf("released after %d sweeps, before the decay's %d", sweeps, fusion.DemoteSweeps)
		}
		if id := fusionFind(base); id >= 0 && fusion.Chunk(id).Base != base {
			t.Errorf("the released chunk's address is still claimed by the hook (cell %d)", id)
		}
		return nil
	})
	for range 3 {
		runtime.GC()
	}
	if wp.Value() != nil {
		t.Error("the released chunk was not collected: still pinned or referenced")
	}
}

// The hook finds objects in a chunk that straddles a 1 MiB address window, in both
// windows, and frees them through it; nothing just outside the chunk is claimed.
func TestFusedBytesStraddle(t *testing.T) {
	fusionOn(t)
	defer fusion.PauseSweeper(fusion.PauseSweeper(true)) // its sweeps would change the counts
	Do(func() error {
		var keep []*Object
		defer func() {
			for _, o := range keep {
				o.Close()
			}
		}()
		victim := -1
		for range 32 {
			id, objs, other := fillChunk(t, 1000)
			keep = append(append(keep, other...), objs...)
			if fusion.Chunk(id).Base%(1<<20) != 0 {
				victim = id
				break
			}
		}
		if victim < 0 {
			t.Skip("every chunk was 1 MiB aligned")
		}
		base := fusion.Chunk(victim).Base
		split := (base>>20+1)<<20 - base // the second window's offset in the chunk
		var first, second *Object
		for i, o := range keep {
			if cellOf(o.Ptr()) != victim {
				continue
			}
			switch off := o.Ptr() - base; {
			case off < split && first == nil:
				first, keep[i] = o, nil
			case off >= split && second == nil:
				second, keep[i] = o, nil
			}
		}
		if first == nil || second == nil {
			t.Fatal("no object in one of the windows (setup)")
		}
		kept := keep[:0]
		for _, o := range keep {
			if o != nil {
				kept = append(kept, o)
			}
		}
		keep = kept
		for name, o := range map[string]*Object{"first": first, "second": second} {
			if fusionFind(o.Ptr()) != victim {
				t.Errorf("an object in the chunk's %s window is not found by the hook", name)
			}
			live := chunkLive(victim)
			o.Close()
			if chunkLive(victim) != live-1 {
				t.Errorf("freeing the object in the %s window did not reach the hook", name)
			}
		}
		for _, p := range []uintptr{base - 16, base + fusion.ChunkSize} {
			if fusionFind(p) == victim {
				t.Errorf("%#x, outside the chunk at %#x, is claimed by it", p, base)
			}
		}
		return nil
	})
}

// The sweeper gives empty chunks back to Go with no Python collection at all: with the GIL
// it releases them; free-threaded, it runs a collection for their grace once more chunks
// wait than the idle keep and none ran for a while.
func TestFusedBytesSweeper(t *testing.T) {
	fusionOn(t)
	Do(func() error {
		defer gcDisable(t)()
		// Five chunks of five classes, filled with Python objects that are then freed.
		var objs []*Object
		for _, size := range []int{600, 6000, 12000, 24000, 48000} {
			_, o, other := fillChunk(t, size)
			objs = append(append(objs, o...), other...)
		}
		for _, o := range objs {
			o.Close()
		}
		return nil
	})
	before := fusion.ReadStats()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) && fusion.ReadStats().Releases-before.Releases < 3 {
		time.Sleep(100 * time.Millisecond)
	}
	after := fusion.ReadStats()
	t.Logf("before %+v, after %+v", before, after)
	if after.Releases-before.Releases < 3 {
		t.Fatalf("the sweeper released %d chunks, want at least 3", after.Releases-before.Releases)
	}
	if freeThread && after.CollectCalls == before.CollectCalls {
		t.Errorf("chunks waiting for a grace were released without a collection")
	}
	if !freeThread && after.Collections != before.Collections {
		t.Errorf("a collection ran: with the GIL the sweeper needs none")
	}
}

// Grow moves a body to a larger class with its contents and gives the old slot back.
func TestFusedBytesGrow(t *testing.T) {
	fusionOn(t)
	defer fusion.PauseSweeper(fusion.PauseSweeper(true)) // its sweeps would change the counts
	fb := fusion.New(4096)
	copy(fb.Buf(), bytes.Repeat([]byte{'g'}, 4096))
	fb.Truncate(4096)
	old, id := fb.Addr(), fb.Cell()
	live := chunkLive(id)
	moves := fusion.ReadStats().Moves
	nb := fb.Grow(16384)
	if nb == nil || nb.Len() != 4096 || !bytes.Equal(nb.Buf()[:4096], bytes.Repeat([]byte{'g'}, 4096)) {
		t.Fatalf("grown body wrong")
	}
	if chunkLive(id) != live-1 || fusion.ReadStats().Moves != moves+1 {
		t.Errorf("the old slot was not given back, or the move not counted")
	}
	again := fusion.New(4096)
	if again.Addr() != old {
		t.Errorf("the old slot was not reused")
	}
	again.Release()
	nb.Release()
}

// The stress test: threads read fused bodies out of a shared dict and list without locks
// while others replace them, and the slots are reused at once. Every body read must be
// whole: its id, then a pattern of it. It is not expected to fail either way: the window
// is a few nanoseconds (arena-design.md section 10); TestFusedBytesStaleReader is the
// deterministic test.
func TestFusedBytesLockFreeStress(t *testing.T) {
	fusionOn(t)
	if !freeThread {
		t.Skip("lock-free readers exist only on free-threaded builds")
	}
	var g *Object
	Do(func() error {
		g = dispatchGlobals(t, `
import threading, gc
def make(i):
    return body(i)
def check(b):
    if len(b) < 16:
        return "short %d" % len(b)
    i = int.from_bytes(b[:8], "little")
    want = make_ref(i)
    if b != want:
        return "body %d corrupt" % i
    return None
def run(nreaders, nwriters, rounds, keys):
    d = {k: make(k) for k in range(keys)}
    l = [make(k) for k in range(keys)]
    stop = threading.Event()
    errors = []
    def reader():
        while not stop.is_set():
            for k in range(keys):
                e = check(d[k]) or check(l[k])
                if e:
                    errors.append(e)
                    stop.set()
                    return
    def writer(w):
        n = keys + w
        for r in range(rounds):
            k = r % keys
            d[k] = make(n)
            l[k] = make(n + 1)
            n += nwriters * 2
    def collector():
        while not stop.is_set():
            gc.collect()
            stop.wait(0.002)
    ts = [threading.Thread(target=reader) for _ in range(nreaders)]
    ts.append(threading.Thread(target=collector))
    ws = [threading.Thread(target=writer, args=(w,)) for w in range(nwriters)]
    for t in ts + ws:
        t.start()
    for t in ws:
        t.join()
    stop.set()
    for t in ts:
        t.join()
    return errors
`, map[string]Func{
			"body": func(a []*Object, _ Kwargs) (any, error) {
				i, _ := a[0].Int64()
				data := fusionBody(i)
				fb := fusion.New(len(data))
				if fb == nil {
					return data, nil // no slot: copied
				}
				copy(fb.Buf(), data)
				return fb, nil
			},
			"make_ref": func(a []*Object, _ Kwargs) (any, error) {
				i, _ := a[0].Int64()
				return fusionBody(i), nil
			},
		})
		return nil
	})
	defer Do(func() error { g.Close(); return nil })
	var errs []string
	Do(func() error {
		run, _ := g.Item("run")
		defer run.Close()
		r, err := run.Call(6, 4, 100000, 64)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := r.Go()
		r.Close()
		for _, e := range v.([]any) {
			errs = append(errs, e.(string))
		}
		return nil
	})
	t.Logf("%+v", fusion.ReadStats())
	Do(func() error { // the next tests start with every chunk past its grace
		gcCollect(t)
		gcCollect(t)
		return nil
	})
	if len(errs) > 0 {
		t.Errorf("%d corrupt reads, first: %s", len(errs), errs[0])
	}
}

// fusionBody is body i: its id (8 bytes) and a pattern of it, 40 to 3000 bytes long, so
// that reused slots of a class get bodies of other lengths.
func fusionBody(i int64) []byte {
	n := 40 + int(i*131%2960)
	b := make([]byte, n)
	for k := range 8 {
		b[k] = byte(i >> (8 * k))
	}
	for k := 8; k < n; k++ {
		b[k] = byte(int64(k)*31 + i)
	}
	return b
}

// tracemalloc started at run time wraps the fusion's hook: fused objects (never traced)
// are created and freed under it, its snapshots work, and stopping it leaves the hook.
func TestFusedBytesTracemalloc(t *testing.T) {
	fusionOn(t)
	Do(func() error {
		r, err := Eval("__import__('tracemalloc').start()")
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
		for i := range 1000 {
			o := fused(t, make([]byte, 100+i))
			o.Close()
		}
		r, err = Eval("len(__import__('tracemalloc').take_snapshot().traces) > 0")
		if err != nil {
			t.Fatal(err)
		}
		if ok, _ := r.Truth(); !ok {
			t.Error("tracemalloc recorded nothing")
		}
		r.Close()
		r, _ = Eval("__import__('tracemalloc').stop()")
		r.Close()
		if !fusionHooked() {
			t.Error("tracemalloc.stop() removed the fusion's hook")
		}
		return nil
	})
}

// fusionChild runs this test binary as sys.executable (py.Main with the tests' config)
// with args and the extra environment; it returns the output and the exit status.
func fusionChild(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), append([]string{executableEnv + "=" + exe}, env...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// Tracemalloc started at initialization (-X tracemalloc) starts in the main phase, after
// the fusion installs its hook (between the phases): tracemalloc wraps the hook, and its
// stop() restores the hook, not the allocator below it. A fused object freed afterwards
// still reaches the hook.
func TestFusedBytesTracemallocAtInit(t *testing.T) {
	out, code := fusionChild(t, []string{"PYTHAGO_TEST_FUSION=1"}, "-X", "tracemalloc", "-c", `
import tracemalloc, _fusiontest as f
assert f.on(), "the fusion is off in the child"
assert tracemalloc.is_tracing(), "tracemalloc did not start at initialization"
tracemalloc.stop()
assert f.hooked(), "tracemalloc.stop() removed the fusion's hook"
b = f.fused(3000)
a = id(b)
n = f.live(a)
assert n >= 1, "the fused object is not in a chunk"
del b
assert f.live(a) == n - 1, "the fused object's free did not reach the hook"
assert f.takes_slot(3000, a), "the freed slot was not taken again"
print("tracemalloc at init: ok")
`)
	if code != 0 || !strings.Contains(out, "tracemalloc at init: ok") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// -X dev runs CPython on its allocators' debug hooks, installed at preinitialization: the
// fusion's hook wraps them. Init (with the startup check, free-threaded) succeeds, a fused
// object freed by Python still reaches the hook and its slot is taken again, and every
// other pointer still goes to the debug hooks, which abort on one they did not allocate.
func TestFusedBytesDevMode(t *testing.T) {
	out, code := fusionChild(t, []string{"PYTHAGO_TEST_FUSION=1"}, "-X", "dev", "-c", `
import gc, sys, gocfg, _fusiontest as f
assert sys.flags.dev_mode and gocfg.debug_hooks(), (sys.flags.dev_mode, gocfg.debug_hooks())
assert f.on() and f.hooked(), "the fusion is off, or its hook is not installed"
b = f.fused(3000)
a = id(b)
n = f.live(a)
assert n >= 1, "the fused object is not in a chunk"
assert b[:3] == b"fff" and len(b + b"x") == 3001 and hash(b) == hash(bytes(b))
del b
assert f.live(a) == n - 1, "the fused object's free did not reach the hook"
assert f.takes_slot(3000, a), "the freed slot was not taken again"
x = [bytes(range(i % 256)) * 3 for i in range(20000)]
del x
gc.collect()
print("dev mode: ok")
`)
	if code != 0 || !strings.Contains(out, "dev mode: ok") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// With the fusion off, nothing is installed: the OBJ allocator's free is not the hook,
// no allocator exists and no chunk is held.
func TestFusedBytesOff(t *testing.T) {
	if fusion.On() {
		t.Skip("the fusion is on (PYTHAGO_TEST_FUSION=1)")
	}
	if fusionHooked() {
		t.Error("the fusion's hook is installed with the fusion off")
	}
	if fusion.Cells() != 0 || fusion.ReadStats() != (fusion.Stats{}) || fusion.New(100) != nil {
		t.Error("the fusion's allocator exists with the fusion off")
	}
	Do(func() error {
		o, _ := From(bytes.Repeat([]byte{'x'}, 3000))
		defer o.Close()
		if fusionFind(o.Ptr()) >= 0 {
			t.Error("the hook's lookup claims a bytes object with the fusion off")
		}
		return nil
	})
}

// Init fails cleanly when the image's CPython is not one the fusion is verified on (the
// check forced to say so), and only when the fusion was asked for.
func TestFusedBytesUnsupported(t *testing.T) {
	out, code := fusionChild(t, []string{"PYTHAGO_TEST_FUSION=1", "PYTHAGO_TEST_FUSION_UNSUPPORTED=1"}, "-c", "print('ran')")
	if code != 1 || !strings.Contains(out, "py: Config.FusedBytes: the native image's CPython is not one the fusion is verified on") || strings.Contains(out, "ran") {
		t.Errorf("fusion on, unverified CPython: exit %d:\n%s", code, out)
	}
	out, code = fusionChild(t, []string{"PYTHAGO_TEST_FUSION=0", "PYTHAGO_TEST_FUSION_UNSUPPORTED=1"}, "-c", "print('ran')")
	if code != 0 || !strings.Contains(out, "ran") {
		t.Errorf("fusion off, unverified CPython: exit %d:\n%s", code, out)
	}
}

// In a subinterpreter the conversion copies: its objects are its own, and its collections
// are not the main interpreter's stop-the-world. The slot goes back.
func TestFusedBytesSubinterpreter(t *testing.T) {
	fusionOn(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	Do(func() error {
		fb := fusion.New(3000)
		copy(fb.Buf(), bytes.Repeat([]byte{'s'}, 3000))
		id, live := fb.Cell(), chunkLive(fb.Cell())
		main := call0(symbol("PyThreadState_Get"))
		sub := call0(symbol("Py_NewInterpreter"))
		if sub == 0 {
			t.Fatal("Py_NewInterpreter failed")
		}
		p, err := fromFused(fb)
		var inChunk bool
		var got []byte
		if err == nil {
			inChunk = cellOf(p) >= 0
			got, _ = borrow(p).Bytes()
			decRef(p)
		}
		call1(symbol("Py_EndInterpreter"), sub)
		call1(symbol("PyThreadState_Swap"), main)
		if err != nil {
			t.Fatal(err)
		}
		if inChunk {
			t.Error("a subinterpreter's bytes object was adopted in a slot")
		}
		if !bytes.Equal(got, bytes.Repeat([]byte{'s'}, 3000)) {
			t.Error("the subinterpreter's copy is wrong")
		}
		if chunkLive(id) != live-1 {
			t.Error("the slot was not given back")
		}
		return nil
	})
}
