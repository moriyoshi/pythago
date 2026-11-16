package fusion

import (
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
	"weak"
)

// The allocator without Python: Python's frees are hookFree, a Go copy of the glue's free
// hook (fusion_find and fusion_release in glue/pythago_py.c) that reads the same shared
// cells, table and bounds the C hook reads.

// newTest makes an allocator for a test (not installed). Its pins are dropped at the end:
// a Pinner collected while pinning panics.
func newTest(t testing.TB, chunks int, freeThreaded bool) *allocator {
	t.Helper()
	a, err := newAllocator(Options{Cap: chunks * ChunkSize, Header: 48, FreeThreaded: freeThreaded})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		for i := range a.chunks {
			a.chunks[i].pin.Unpin()
		}
		a.pin.Unpin()
	})
	return a
}

// find is the hook's lookup: the cell whose chunk holds p, and p's offset in it. It reads
// what C reads (bounds, l1, the entry, the cell's base); Go's typed views of the same
// arrays stand in for C's pointer loads (checkptr), checked against the addresses C uses.
func (a *allocator) find(p uintptr) (int, uintptr) {
	if p < a.bounds[0].Load() || p >= a.bounds[1].Load() {
		return -1, 0
	}
	w := p >> windowShift
	addr := atomic.LoadUintptr(&a.l1[w>>l2Bits])
	if addr == 0 {
		return -1, 0
	}
	l2 := a.l2[w>>l2Bits]
	if uintptr(unsafe.Pointer(l2)) != addr {
		panic("l1 and Go's l2 disagree")
	}
	e := atomic.LoadUint64(&l2[w&(1<<l2Bits-1)])
	for ; e != 0; e >>= 32 {
		id := uint32(e)
		if id == 0 {
			continue
		}
		if off := p - a.cells[id-1].base.Load(); off < ChunkSize {
			return int(id - 1), off
		}
	}
	return -1, 0
}

// hookFree is the glue's free of p by Python: false if p is not in a chunk (the original
// allocator's).
func (a *allocator) hookFree(p uintptr) bool {
	id, off := a.find(p)
	if id < 0 {
		return false
	}
	st := &a.cells[id]
	idx := uint32(off>>st.dshift.Load()) * st.dinv.Load()
	e := a.epoch.Load()
	for s := st.stamp.Load(); s < e && !st.stamp.CompareAndSwap(s, e); s = st.stamp.Load() {
	}
	links := a.chunks[id].links
	if uintptr(unsafe.Pointer(&links[0])) != st.links.Load() {
		panic("the cell's links and Go's disagree")
	}
	for {
		h := st.head.Load()
		atomic.StoreUint32(&links[idx], uint32(h))
		if st.head.CompareAndSwap(h, (h>>32+1)<<32|uint64(idx+1)) {
			break
		}
	}
	st.live.Add(-1)
	return true
}

// adopt stands for py's conversion: the slot now holds a Python object.
func adopt(t testing.TB, b *Bytes) uintptr {
	t.Helper()
	p, _, ok := b.Claim()
	if !ok {
		t.Fatal("Claim failed")
	}
	return uintptr(p)
}

// Every body length maps to the smallest class that holds it; a class's slot holds the
// header, the capacity and the NUL, in 16-byte steps; and the hook's division-free slot
// index (offset >> dshift) * dinv is the slot index for every slot of every class.
func TestClasses(t *testing.T) {
	caps := Caps()
	if len(caps) != 44 || caps[0] != 16 || caps[len(caps)-1] != MaxCap {
		t.Fatalf("caps: %d classes, %v", len(caps), caps)
	}
	for n := 1; n <= MaxCap; n++ {
		ci := ClassFor(n)
		if ci >= len(caps) || caps[ci] < n || ci > 0 && caps[ci-1] >= n {
			t.Fatalf("body %d: class %d", n, ci)
		}
	}
	for _, header := range []int{32, 48} {
		a, err := newAllocator(Options{Cap: ChunkSize, Header: header})
		if err != nil {
			t.Fatal(err)
		}
		for ci, cl := range a.classes {
			if cl.size < header+cl.cap+1 || cl.size%16 != 0 || cl.size-(header+cl.cap+1) >= 16 {
				t.Errorf("header %d, class %d: cap %d, slot %d", header, ci, cl.cap, cl.size)
			}
			k := trailingZeros(cl.size)
			inv := oddInverse(uint32(cl.size >> k))
			for i := 0; i < ChunkSize/cl.size; i++ {
				if got := uint32((i*cl.size)>>k) * inv; got != uint32(i) {
					t.Fatalf("header %d, class %d: slot %d indexes as %d", header, ci, i, got)
				}
			}
		}
		a.pin.Unpin()
	}
}

func trailingZeros(n int) int {
	k := 0
	for n%2 == 0 {
		n, k = n/2, k+1
	}
	return k
}

// A slot Python frees goes back on its chunk's free stack and is taken again at once, in
// its class (LIFO); a slot Go releases unconverted too.
func TestReuse(t *testing.T) {
	a := newTest(t, 2, true)
	b := a.new(3000)
	p := adopt(t, b)
	if !a.hookFree(p) {
		t.Fatal("the hook did not claim a fused object")
	}
	b2 := a.new(2900) // same class
	if b2.Addr() != p {
		t.Fatalf("the freed slot was not taken again: %#x, want %#x", b2.Addr(), p)
	}
	b2.Release()
	if b3 := a.new(3000); b3.Addr() != p {
		t.Fatal("the released slot was not taken again")
	}
	if a.hookFree(a.cells[b.id].base.Load()-16) || a.hookFree(0x1000) {
		t.Error("the hook claimed an address outside every chunk")
	}
}

// Python threads free (push, through the hook) while Go takes (pop, under its mutex): no
// slot is ever handed out twice at once, and every free is seen.
func TestConcurrentPushPop(t *testing.T) {
	a := newTest(t, 4, true)
	const workers, rounds = 8, 100000
	owned := map[uintptr]*atomic.Bool{}
	var omu sync.Mutex
	owner := func(p uintptr) *atomic.Bool {
		omu.Lock()
		defer omu.Unlock()
		o := owned[p]
		if o == nil {
			o = new(atomic.Bool)
			owned[p] = o
		}
		return o
	}
	var wg sync.WaitGroup
	var bad atomic.Int64
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 1))
			var held []uintptr
			for range rounds {
				if len(held) < 8 && r.IntN(2) == 0 {
					sizes := []int{100, 3000, 3100} // two classes, one shared
					b := a.new(sizes[r.IntN(len(sizes))])
					if b == nil {
						continue
					}
					p := adopt(t, b)
					if !owner(p).CompareAndSwap(false, true) {
						bad.Add(1)
					}
					held = append(held, p)
				} else if len(held) > 0 {
					i := r.IntN(len(held))
					p := held[i]
					held = append(held[:i], held[i+1:]...)
					owner(p).Store(false)
					if !a.hookFree(p) {
						bad.Add(1)
					}
				}
			}
			for _, p := range held {
				owner(p).Store(false)
				a.hookFree(p)
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d slots handed out twice at once, or frees unclaimed", bad.Load())
	}
	for id := range a.cells {
		if l := a.cells[id].live.Load(); l != 0 {
			t.Errorf("cell %d: live %d after every slot was freed", id, l)
		}
	}
}

// A chunk leaves its class only at live 0 and, free-threaded, past its grace: Python freed
// nothing in it since it was assigned, or a collection began after its last free. With the
// GIL, at live 0.
func TestReclaimGrace(t *testing.T) {
	for _, ft := range []bool{true, false} {
		a := newTest(t, 2, ft)
		// Class A fills chunk 0 with "Python objects"; class B (64 KiB) takes chunk 1.
		var objs []uintptr
		b := a.new(1000)
		victim := int(b.id)
		objs = append(objs, adopt(t, b))
		for a.chunks[victim].carved < a.chunks[victim].nslots {
			objs = append(objs, adopt(t, a.new(1000)))
		}
		held := a.new(MaxCap)
		if held == nil || int(held.id) == victim {
			t.Fatal("setup: class B did not get the other chunk")
		}
		for _, p := range objs[1:] {
			a.hookFree(p)
		}
		// One live slot: never reclaimed, whatever the grace.
		a.gcStart()
		a.gcStop()
		for a.new(MaxCap) != nil {
		}
		if a.chunks[victim].class == noClass {
			t.Fatalf("ft %v: a chunk with a live slot left its class", ft)
		}
		a.hookFree(objs[0]) // live 0, stamped
		got := a.new(MaxCap)
		if ft && got != nil {
			t.Fatalf("ft: the chunk changed class before a collection")
		}
		if !ft {
			if got == nil || int(got.id) != victim {
				t.Fatalf("GIL: the chunk was not reclaimed at live 0")
			}
			continue
		}
		a.gcStart()
		a.hookFree(adopt(t, a.new(1000))) // a free after the start: not past the grace yet
		a.gcStop()
		if a.new(MaxCap) != nil {
			t.Fatalf("ft: the chunk changed class with a free after the collection's start")
		}
		a.gcStart()
		a.gcStop()
		if got := a.new(MaxCap); got == nil || int(got.id) != victim {
			t.Fatalf("ft: the chunk was not reclaimed past its grace")
		}
		if a.classChanges.Load() != 1 {
			t.Errorf("ft: %d class changes, want 1", a.classChanges.Load())
		}
	}
}

// A chunk whose slots Go took and released unconverted (stamp 0) needs no grace.
func TestReclaimUnstamped(t *testing.T) {
	a := newTest(t, 1, true)
	b := a.new(1000)
	b.Release()
	if got := a.new(MaxCap); got == nil {
		t.Fatal("a chunk Python never freed into waited for a grace")
	}
}

// The hook finds objects in a chunk that straddles a 1 MiB window, in both windows, and
// nothing just outside it.
func TestLookupStraddle(t *testing.T) {
	a := newTest(t, 64, true)
	id := -1
	for range 64 {
		b := a.new(MaxCap)
		if b == nil {
			break
		}
		if base := a.cells[b.id].base.Load(); base%(1<<windowShift) != 0 {
			id = int(b.id)
			break
		}
	}
	if id < 0 {
		t.Skip("every chunk was 1 MiB aligned")
	}
	base := a.cells[id].base.Load()
	split := (base>>windowShift+1)<<windowShift - base // the offset of the second window
	for _, off := range []uintptr{0, split - 16, split, split + 16, ChunkSize - 16} {
		if got, goff := a.find(base + off); got != id || goff != off {
			t.Errorf("chunk at %#x: offset %#x not found (window boundary at %#x)", base, off, split)
		}
	}
	for _, p := range []uintptr{base - 16, base + ChunkSize} {
		if got, _ := a.find(p); got == id {
			t.Errorf("%#x, outside the chunk at %#x, was claimed by it", p, base)
		}
	}
}

// A chunk goes back to Go (unregistered, unpinned, dropped) once it is empty and past its
// grace, after the decay's sweeps, and not before; its addresses are then no longer
// claimed by the hook.
func TestRelease(t *testing.T) {
	a := newTest(t, 4, true)
	b := a.new(1000)
	id := b.id
	p := adopt(t, b)
	base := a.cells[id].base.Load()
	wp := weak.Make(&a.chunks[id].mem[0])
	a.hookFree(p) // stamped: waits for a grace
	b = nil
	for range DemoteSweeps + IdleSweeps + 2 {
		a.mu.Lock()
		a.sweep()
		a.mu.Unlock()
	}
	if a.chunks[id].mem == nil || a.chunks[id].class == noClass {
		t.Fatal("a chunk left its class with no collection after its last free")
	}
	a.gcStart()
	a.gcStop() // the grace
	sweep := func() {
		a.mu.Lock()
		a.sweep()
		a.mu.Unlock()
	}
	sweep()
	if a.chunks[id].class == noClass {
		t.Fatal("demoted after one sweep")
	}
	n := 1
	for ; a.chunks[id].mem != nil && n < DemoteSweeps+IdleSweeps+2; n++ {
		sweep()
	}
	if a.chunks[id].mem != nil {
		t.Fatalf("not released after %d sweeps past its grace", n)
	}
	if n != DemoteSweeps+IdleSweeps-1 {
		t.Errorf("released after %d sweeps, want %d", n, DemoteSweeps+IdleSweeps-1)
	}
	if got, _ := a.find(base); got >= 0 {
		t.Error("the released chunk's address is still claimed by the hook")
	}
	if a.bounds[0].Load() <= base && base < a.bounds[1].Load() {
		t.Error("the bounds still cover the released chunk")
	}
	for range 3 {
		runtime.GC()
	}
	if wp.Value() != nil {
		t.Error("the released chunk was not collected: still pinned or referenced")
	}
	if a.releases.Load() != 1 || len(a.free) != 4 {
		t.Errorf("releases %d, free cells %d", a.releases.Load(), len(a.free))
	}
}

// Idle chunks beyond the newest idleKeep go back after demoteSweeps sweeps idle; the
// newest idleKeep stay until idleSweeps. An idle chunk is reused before a new one is made.
func TestIdleKeep(t *testing.T) {
	a := newTest(t, 8, false)
	var bs []*Bytes
	for _, n := range []int{100, 1000, 10000, 40000, MaxCap} { // five classes, five chunks
		bs = append(bs, a.new(n))
	}
	for _, b := range bs {
		b.Release()
	}
	sweep := func(n int) {
		for range n {
			a.mu.Lock()
			a.sweep()
			a.mu.Unlock()
		}
	}
	sweep(DemoteSweeps)
	if len(a.idle) != 5 || a.releases.Load() != 0 {
		t.Fatalf("after %d sweeps: idle %d, released %d; want 5, 0", DemoteSweeps, len(a.idle), a.releases.Load())
	}
	sweep(DemoteSweeps - 2)
	if a.releases.Load() != 0 {
		t.Fatalf("extra idle chunks released after %d sweeps idle", DemoteSweeps-1)
	}
	sweep(1)
	if len(a.idle) != IdleKeep || a.releases.Load() != 3 {
		t.Fatalf("idle %d, released %d; want %d, 3", len(a.idle), a.releases.Load(), IdleKeep)
	}
	allocs := a.allocs.Load()
	b := a.new(200)
	if a.allocs.Load() != allocs || a.chunks[b.id].class != ClassFor(200) {
		t.Fatal("a new chunk was made with idle ones held")
	}
	b.Release()
}

// The table keeps both halves of a window that two chunks share, through registration
// and unregistration in any order.
func TestTableShared(t *testing.T) {
	a := newTest(t, 2, false)
	base := uintptr(0x40_0000_2000) // unaligned: spans windows w and w+1
	a.mu.Lock()
	a.register(0, base)
	a.register(1, base+ChunkSize) // spans w+1 and w+2
	a.mu.Unlock()
	for _, c := range []struct {
		p  uintptr
		id int
	}{{base, 0}, {base + ChunkSize - 1, 0}, {base + ChunkSize, 1}, {base + 2*ChunkSize - 1, 1}} {
		if got, _ := a.find(c.p); got != c.id {
			t.Errorf("%#x: not found in cell %d", c.p, c.id)
		}
	}
	a.mu.Lock()
	a.unregister(0)
	a.mu.Unlock()
	if got, _ := a.find(base); got >= 0 {
		t.Error("unregistered chunk 0 still found")
	}
	if got, _ := a.find(base + ChunkSize); got != 1 {
		t.Error("chunk 1 lost with chunk 0's unregistration")
	}
	a.mu.Lock()
	a.unregister(1)
	a.mu.Unlock()
	if a.bounds[0].Load() != ^uintptr(0) || a.bounds[1].Load() != 0 {
		t.Error("bounds not empty with no chunk")
	}
}

// The sweeper releases empty chunks with no collection (GIL), and free-threaded runs one
// for their grace once more wait than idleKeep and none ran for collectAfter ticks; not
// for a single empty chunk.
func TestSweeper(t *testing.T) {
	for _, ft := range []bool{false, true} {
		a := newTest(t, 8, ft)
		old := Collect
		var calls atomic.Int64
		Collect = func() { calls.Add(1); a.gcStart(); a.gcStop() }
		tick := make(chan time.Time)
		done := make(chan struct{})
		go func() { a.sweeper(tick); close(done) }()
		ticks := func(n int) {
			for range n {
				tick <- time.Time{}
			}
		}
		// One empty, stamped chunk: kept, and no collection for it.
		a.hookFree(adopt(t, a.new(100)))
		ticks(collectAfter + demoteSweeps + 2)
		time.Sleep(10 * time.Millisecond)
		if ft && calls.Load() != 0 {
			t.Errorf("ft: a collection ran for a single empty chunk")
		}
		// Four more classes' chunks, emptied by Python.
		for _, n := range []int{1000, 5000, 20000, 60000} {
			a.hookFree(adopt(t, a.new(n)))
		}
		for i := 0; i < 2*collectAfter+idleSweeps+4 && a.releases.Load() < 3; i++ {
			ticks(1)
			time.Sleep(time.Millisecond) // a collection runs on its own goroutine
		}
		close(tick)
		<-done
		Collect = old
		if a.releases.Load() < 3 {
			t.Errorf("ft %v: %d chunks released, want at least 3", ft, a.releases.Load())
		}
		if ft != (calls.Load() > 0) {
			t.Errorf("ft %v: %d collections run", ft, calls.Load())
		}
	}
}
