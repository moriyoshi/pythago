// Package fusion is the allocator behind Go-owned bytes objects (py.Config.FusedBytes):
// Go reads a request body into a slot of Go memory laid out as a Python bytes object, and
// package py turns that slot into the bytes object in place (the glue's
// pythago_bytes_adopt), with no copy. Python frees the object through PyObject_Free, which
// the glue's allocator hook routes back here. The design and its measurements are
// .agents/docs/arena-design.md, sections 13 to 15.
//
// The package is pure Go: chunks of Go memory, size classes, the Go side of the per-chunk
// free stacks, the lookup table the C hook reads, class assignment and the release of
// chunks back to Go. Package py installs it (Install), the glue's hook and gc callback,
// and converts a *Bytes; package asgi takes request bodies from it (New).
//
// # Chunks
//
// Memory comes in chunks of ChunkSize, each its own Go allocation (make), made when a
// class needs one, up to the cap (Options.Cap). A chunk in use belongs to one size class
// and is carved into equal slots: a bytes header (up to ob_sval), a payload capacity and
// the NUL, rounded to 16 bytes. Each chunk is pinned (runtime.Pinner) while it is
// registered for the C hook; a released chunk is unregistered, unpinned and dropped, and
// Go's garbage collector reclaims it.
//
// A sweeper goroutine (Sweeper) runs once a second: a chunk found empty and past its grace
// at demoteSweeps sweeps in a row becomes idle (any class may take it); the newest
// idleKeep idle chunks go back to Go after idleSweeps sweeps, the others after
// demoteSweeps. A busy server's Python collections are rare, so on free-threaded builds
// the sweeper runs one itself (Collect) when more empty chunks than idleKeep wait for a
// grace and none ran for collectAfter sweeps.
//
// # The invariant
//
// Once a slot has held an object, nothing but adoption writes its bytes before ob_type:
// Go writes payload only from ob_sval on, and the free stacks' links live in a separate
// Go array, never in a slot. A freed slot keeps the dead header Python left (ob_ref_local
// 0, ob_ref_shared 0 or _Py_REF_MERGED), so on free-threaded builds a lock-free reader
// that loaded the object's pointer before its free fails to incref it without writing;
// once the slot holds a new object at the same address, the reader increfs a live header,
// sees that its container changed, and decrefs. That is what mimalloc gives CPython within
// a size class, so a freed slot is reused at once, within its class.
//
// Header addresses move only when a chunk leaves its class: it becomes idle (reusable for
// any class) or goes back to Go. That needs no live slot in the chunk and, on
// free-threaded builds, a garbage collection's stop-the-world that began after its last
// free by Python (its stamp is below the safe epoch): no reader can hold a pointer into it
// then. The gc callback publishes the epochs (GCStart, GCStop); when every chunk is held
// and one waits only for that grace, a background collection (Collect) provides one.
//
// # The C hook's lookup
//
// The hook (glue: fusion_find) sees every PyObject_Free and PyObject_Realloc. It first
// compares the pointer with the lowest and highest registered chunk addresses (bounds),
// which rejects almost every free: Python's own object memory (pymalloc's arenas on 3.13,
// mimalloc's segments on 3.14t) is mmapped by Python, never Go heap. Then it reads a
// two-level radix table keyed by the 1 MiB address window (p >> windowShift): l1 holds
// the address of an l2 array or 0, an l2 entry the cell indices (+1) of the at most two
// chunks that overlap the window (a chunk is only 8 KiB-aligned, so it may span two
// windows; a window may hold the end of one and the start of the next). The hook checks
// p - base < ChunkSize on each. That is a constant number of loads and no lock; Go writes
// the table under its mutex, with atomic stores.
//
// Why the lookup has no race: the hook is only ever called with a pointer to a live
// object. For a fused object, its chunk was registered (base, then the window entries,
// then the bounds) before the slot was handed out, and its pointer reached the freeing
// thread through Python's own synchronization, so the hook sees the registration. The
// chunk stays registered while the object lives: Go unregisters a chunk only at live 0
// and past its grace, and an entry change for another chunk sharing a window is one
// atomic store that keeps this chunk's half. For any other pointer, every chunk base the
// hook may read (current, stale or 0) is Go memory that never holds a Python object it
// did not adopt, so p - base < ChunkSize is false whatever it reads.
package fusion

import (
	"errors"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	// ChunkSize is the size of a chunk: one Go allocation, carved into slots of one class.
	ChunkSize = 1 << 20
	// MaxCap is the largest class's payload capacity: longer bodies are not fused.
	MaxCap = 64 << 10

	windowShift = 20                              // the hook's lookup window: 1 MiB (= ChunkSize)
	addrBits    = 48                              // Go heap addresses are below 2^48 (64-bit)
	l2Bits      = 14                              // windows per l2 array
	l1Bits      = addrBits - windowShift - l2Bits // l2 arrays
	noClass     = -1
)

// The idle chunks' decay, as internal/arena's SharedLane keeps a chunk: a chunk of a
// class found empty and past its grace by demoteSweeps sweeps in a row becomes idle; idle
// chunks beyond the newest idleKeep go back to Go after demoteSweeps sweeps idle, and the
// others after idleSweeps.
const (
	demoteSweeps = 4
	idleKeep     = 2
	idleSweeps   = 16
)

// The sweeper (Sweeper) sweeps every sweepInterval, so the decay is in seconds; the gc
// callback only publishes the grace. A server's Python collections are rare (gen0 counts
// allocations minus frees, which balance), so sweeps cannot wait for them. Free-threaded,
// empty chunks wait for a grace: when more of them wait than idleKeep and no collection
// ran for collectAfter sweeps, it runs one (Collect).
const (
	sweepInterval = time.Second
	collectAfter  = 10
)

// cell is the C hook's view of a chunk cell (glue: struct pythago_fusion_cell). C stamps
// it, pushes freed slots on head and decrements live; Go writes the rest under mu.
type cell struct {
	head   atomic.Uint64  // free stack: tag<<32 | (slot index + 1); 0 index: empty
	live   atomic.Int64   // slots taken by Go or holding a live object
	stamp  atomic.Uint64  // the epoch of its last free by Python (0: none since assigned)
	base   atomic.Uintptr // the chunk's address; 0: no chunk
	links  atomic.Uintptr // the free stack's links: a []uint32 by slot index
	dshift atomic.Uint32  // slot size = 2^dshift * odd
	dinv   atomic.Uint32  // the odd part's inverse mod 2^32: index = (offset >> dshift) * dinv
}

// cConfig is the glue's struct pythago_fusion_config.
type cConfig struct {
	cells, ncells, chunkSize, l1, epoch uintptr
}

// chunk is Go's own view of a cell, guarded by mu.
type chunk struct {
	mem    []byte // nil: the cell holds no chunk
	links  []uint32
	pin    runtime.Pinner
	class  int // noClass: idle (or no chunk)
	carved int // slots handed out at least once since assigned (bump)
	nslots int
	last   int // the class it last had (class changes are counted against it)
	quiet  int // sweeps in a row that found it empty and past its grace
	empty  int // sweeps in a row that found it empty, past its grace or not
}

type class struct {
	cap    int   // payload capacity
	size   int   // slot size: header + cap + NUL, rounded to 16
	cur    int   // the chunk taken from, or noClass
	chunks []int // its chunks' cells
}

// Options configure Install.
type Options struct {
	// Cap is the most memory chunks may hold, in bytes (rounded up to ChunkSize).
	Cap int
	// Header is offsetof(PyBytesObject, ob_sval): where a slot's payload starts.
	Header int
	// FreeThreaded makes chunks wait for a grace (a collection's stop-the-world) before
	// they leave their class; with the GIL there are no lock-free readers.
	FreeThreaded bool
	// Bounds is where the lowest and highest+1 registered chunk addresses live: two
	// uintptr_t in the glue's static storage (pythago_fusion_bounds), which the hook reads
	// on every free without a dependent load. Nil (tests): the allocator's own.
	Bounds unsafe.Pointer
}

type allocator struct {
	mu      sync.Mutex
	opts    Options
	cells   []cell  // shared with C
	chunks  []chunk // by cell
	classes []class
	free    []int // cells without a chunk
	idle    []int // cells with an idle chunk: no class, past its grace

	l1     []uintptr              // shared with C: l2 arrays' addresses
	l2     []*[1 << l2Bits]uint64 // the same arrays, by l1 index, for Go
	bounds *[2]atomic.Uintptr     // shared with C: lowest and highest+1 registered addresses
	own    [2]atomic.Uintptr      // bounds without Options.Bounds
	pin    runtime.Pinner         // the allocator, cells, l1, l2 arrays

	epoch   atomic.Uint64 // C reads it at every fused free; bumped at a collection's start
	safe    atomic.Uint64 // frees stamped below it are past a full stop-the-world
	started uint64        // the epoch set by the current collection's start (GCStart)
	cfg     cConfig

	noCollect  atomic.Bool // tests: never run Collect
	noSweeper  atomic.Bool // tests: the sweeper skips its sweeps
	collecting atomic.Bool // a Collect runs

	// Counters (Stats).
	takes, moves, collections, collectCalls, classChanges, allocs, releases atomic.Int64
}

// cur is the installed allocator, nil while the fusion is off.
var cur atomic.Pointer[allocator]

// Collect runs a garbage collection (PyGC_Collect) on a Python thread: package py sets it
// at install. The allocator calls it from a goroutine of its own, never under its lock.
var Collect func()

// Install sets the allocator up and returns the glue's configuration (struct
// pythago_fusion_config), which C keeps for the life of the process. It fails if it was
// installed already.
func Install(o Options) (unsafe.Pointer, error) {
	a, err := newAllocator(o)
	if err != nil {
		return nil, err
	}
	if !cur.CompareAndSwap(nil, a) {
		return nil, errors.New("fusion: installed already")
	}
	return unsafe.Pointer(&a.cfg), nil
}

// On reports whether the fusion is installed.
func On() bool { return cur.Load() != nil }

func newAllocator(o Options) (*allocator, error) {
	if o.Cap <= 0 {
		return nil, errors.New("fusion: the cap must be positive")
	}
	if o.Header <= 0 || o.Header%8 != 0 || o.Header > 256 {
		return nil, errors.New("fusion: implausible bytes header size")
	}
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return nil, errors.New("fusion: needs a 64-bit platform")
	}
	n := (o.Cap + ChunkSize - 1) / ChunkSize
	a := &allocator{opts: o}
	a.cells = make([]cell, n)
	a.chunks = make([]chunk, n)
	for i := n - 1; i >= 0; i-- {
		a.chunks[i] = chunk{class: noClass, last: noClass}
		a.free = append(a.free, i)
	}
	for _, c := range Caps() {
		a.classes = append(a.classes, class{cap: c, size: (o.Header + c + 1 + 15) &^ 15, cur: noClass})
	}
	a.l1 = make([]uintptr, 1<<l1Bits)
	a.l2 = make([]*[1 << l2Bits]uint64, 1<<l1Bits)
	a.pin.Pin(a) // C keeps the address of epoch
	a.pin.Pin(&a.cells[0])
	a.pin.Pin(&a.l1[0])
	a.bounds = &a.own
	if o.Bounds != nil {
		a.bounds = (*[2]atomic.Uintptr)(o.Bounds)
	}
	a.bounds[0].Store(^uintptr(0))
	a.bounds[1].Store(0)
	a.epoch.Store(1)
	a.cfg = cConfig{
		cells: uintptr(unsafe.Pointer(&a.cells[0])), ncells: uintptr(n), chunkSize: ChunkSize,
		l1: uintptr(unsafe.Pointer(&a.l1[0])), epoch: uintptr(unsafe.Pointer(&a.epoch)),
	}
	return a, nil
}

// ClassFor returns the class index for a body of n bytes (1 <= n <= MaxCap): 16-byte
// steps to 128, then four classes per doubling.
func ClassFor(n int) int {
	if n <= 128 {
		return (n+15)/16 - 1
	}
	l := bits.Len(uint(n - 1))
	b := 1 << (l - 1)
	return 8 + (l-8)*4 + (n-1-b)/(b/4)
}

// Caps lists the classes' payload capacities, in ClassFor's order.
func Caps() []int {
	var caps []int
	for c := 16; c <= 128; c += 16 {
		caps = append(caps, c)
	}
	for b := 128; b < MaxCap; b *= 2 {
		for k := 1; k <= 4; k++ {
			caps = append(caps, b+k*b/4)
		}
	}
	return caps
}

// oddInverse returns x's inverse mod 2^32 (x odd), by Newton's iteration.
func oddInverse(x uint32) uint32 {
	y := x
	for range 5 {
		y *= 2 - x*y
	}
	return y
}

// pastGrace reports whether no stale reader can hold a pointer into cell id's chunk, given
// that it has no live slot: with the GIL always; free-threaded, if Python freed nothing in
// it since it was assigned, or a stop-the-world began after its last free.
func (a *allocator) pastGrace(id int) bool {
	if !a.opts.FreeThreaded {
		return true
	}
	s := a.cells[id].stamp.Load()
	return s == 0 || s < a.safe.Load()
}

// entry returns the table entry of window w, making its l2 array if create is set (nil
// if there is none). Under mu.
func (a *allocator) entry(w uintptr, create bool) *uint64 {
	i := w >> l2Bits
	if a.l2[i] == nil {
		if !create {
			return nil
		}
		l2 := new([1 << l2Bits]uint64)
		a.pin.Pin(l2)
		a.l2[i] = l2
		atomic.StoreUintptr(&a.l1[i], uintptr(unsafe.Pointer(l2)))
	}
	return &a.l2[i][w&(1<<l2Bits-1)]
}

// register makes chunk id at base findable by the hook: base first, then the window
// entries, then the bounds, so that a hook that finds an entry finds the base. Under mu.
func (a *allocator) register(id int, base uintptr) {
	a.cells[id].base.Store(base)
	for w := base >> windowShift; w <= (base+ChunkSize-1)>>windowShift; w++ {
		e := a.entry(w, true)
		old := atomic.LoadUint64(e)
		tag := uint64(id + 1)
		switch {
		case uint32(old) == 0:
			atomic.StoreUint64(e, old|tag)
		case old>>32 == 0:
			atomic.StoreUint64(e, old|tag<<32)
		default:
			panic("fusion: a window overlaps more than two chunks")
		}
	}
	if base < a.bounds[0].Load() {
		a.bounds[0].Store(base)
	}
	if end := base + ChunkSize; end > a.bounds[1].Load() {
		a.bounds[1].Store(end)
	}
}

// unregister removes chunk id from the hook's lookup: the window entries, then the base;
// the bounds shrink to the chunks left. Under mu; only at live 0 and past its grace.
func (a *allocator) unregister(id int) {
	base := a.cells[id].base.Load()
	tag := uint64(id + 1)
	for w := base >> windowShift; w <= (base+ChunkSize-1)>>windowShift; w++ {
		e := a.entry(w, false)
		old := atomic.LoadUint64(e)
		switch {
		case uint32(old) == uint32(tag):
			atomic.StoreUint64(e, old&^0xffffffff)
		case old>>32 == tag:
			atomic.StoreUint64(e, old&0xffffffff)
		default:
			panic("fusion: unregistering a chunk not in the table")
		}
	}
	a.cells[id].base.Store(0)
	lo, hi := ^uintptr(0), uintptr(0)
	for i := range a.cells {
		if b := a.cells[i].base.Load(); b != 0 {
			lo, hi = min(lo, b), max(hi, b+ChunkSize)
		}
	}
	a.bounds[0].Store(lo)
	a.bounds[1].Store(hi)
}

// newChunk gives a free cell a new chunk of Go memory, pinned and registered. Under mu.
func (a *allocator) newChunk() (int, bool) {
	if len(a.free) == 0 {
		return 0, false
	}
	mem := make([]byte, ChunkSize)
	base := uintptr(unsafe.Pointer(&mem[0]))
	if base+ChunkSize > 1<<addrBits { // never on Go's 64-bit heaps; not representable in the table
		return 0, false
	}
	id := a.free[len(a.free)-1]
	a.free = a.free[:len(a.free)-1]
	c := &a.chunks[id]
	c.pin.Pin(&mem[0])
	c.mem = mem
	a.register(id, base)
	a.allocs.Add(1)
	return id, true
}

// release gives idle chunk id back to Go: unregistered, unpinned and dropped. Under mu.
func (a *allocator) release(id int) {
	a.unregister(id)
	c := &a.chunks[id]
	c.pin.Unpin()
	*c = chunk{class: noClass, last: noClass}
	st := &a.cells[id]
	st.head.Store(0)
	st.stamp.Store(0)
	st.links.Store(0)
	a.free = append(a.free, id)
	a.releases.Add(1)
}

// assign gives chunk id (new, or idle and so past its grace) to class ci. Under mu.
func (a *allocator) assign(id, ci int) {
	c := &a.chunks[id]
	cl := &a.classes[ci]
	if c.last != noClass && c.last != ci {
		a.classChanges.Add(1)
	}
	n := ChunkSize / cl.size
	if cap(c.links) < n {
		c.links = make([]uint32, n)
		c.pin.Pin(&c.links[0])
	}
	c.links = c.links[:n]
	c.class, c.carved, c.nslots, c.last, c.quiet, c.empty = ci, 0, n, ci, 0, 0
	st := &a.cells[id]
	k := bits.TrailingZeros(uint(cl.size))
	st.dshift.Store(uint32(k))
	st.dinv.Store(oddInverse(uint32(cl.size >> k)))
	st.links.Store(uintptr(unsafe.Pointer(&c.links[0])))
	st.head.Store(0)
	st.stamp.Store(0)
	cl.chunks = append(cl.chunks, id)
}

// demote takes chunk id (empty and past its grace) from its class: it becomes idle. Under
// mu.
func (a *allocator) demote(id int) {
	c := &a.chunks[id]
	cl := &a.classes[c.class]
	for i, x := range cl.chunks {
		if x == id {
			cl.chunks = append(cl.chunks[:i], cl.chunks[i+1:]...)
			break
		}
	}
	if cl.cur == id {
		cl.cur = noClass
	}
	c.class, c.quiet, c.empty = noClass, 0, 0
	a.idle = append(a.idle, id)
}

// pop takes a slot index off chunk id's free stack. Under mu: Go is the only popper.
func (a *allocator) pop(id int) (int, bool) {
	st := &a.cells[id]
	links := a.chunks[id].links
	for {
		h := st.head.Load()
		top := uint32(h)
		if top == 0 {
			return 0, false
		}
		nxt := atomic.LoadUint32(&links[top-1])
		if st.head.CompareAndSwap(h, (h>>32+1)<<32|uint64(nxt)) {
			return int(top - 1), true
		}
	}
}

// push gives slot idx of chunk id back without a stamp (Python never freed it), as the
// glue's free hook does. Go releases slots only of chunks it holds a slot of, so the
// chunk cannot change under it.
func (a *allocator) push(id, idx int) {
	st := &a.cells[id]
	link := &a.chunks[id].links[idx]
	for {
		h := st.head.Load()
		atomic.StoreUint32(link, uint32(h))
		if st.head.CompareAndSwap(h, (h>>32+1)<<32|uint64(idx+1)) {
			break
		}
	}
	st.live.Add(-1)
}

// hasRoom reports whether chunk id can give a slot now. Under mu.
func (a *allocator) hasRoom(id int) bool {
	return uint32(a.cells[id].head.Load()) != 0 || a.chunks[id].carved < a.chunks[id].nslots
}

// take takes a slot of class ci: its cell and index. Under mu.
func (a *allocator) take(ci int) (id, idx int, ok bool) {
	cl := &a.classes[ci]
	for {
		if id := cl.cur; id >= 0 {
			if idx, ok := a.pop(id); ok {
				a.cells[id].live.Add(1)
				return id, idx, true
			}
			if c := &a.chunks[id]; c.carved < c.nslots {
				idx := c.carved
				c.carved++
				a.cells[id].live.Add(1)
				return id, idx, true
			}
		}
		found := false
		for _, id := range cl.chunks {
			if id != cl.cur && a.hasRoom(id) {
				cl.cur, found = id, true
				break
			}
		}
		if found {
			continue
		}
		id, ok := a.chunkFor(ci)
		if !ok {
			return 0, 0, false
		}
		a.assign(id, ci)
		cl.cur = id
	}
}

// chunkFor finds a chunk for class ci: an idle one, a new one, or one reclaimed from
// another class. Under mu.
func (a *allocator) chunkFor(ci int) (int, bool) {
	if len(a.idle) == 0 {
		if id, ok := a.newChunk(); ok {
			return id, true
		}
		a.reclaim(ci)
	}
	if n := len(a.idle); n > 0 {
		id := a.idle[n-1]
		a.idle = a.idle[:n-1]
		return id, true
	}
	return 0, false
}

// reclaim makes idle every chunk of another class than ci that has no live slot and is
// past its grace. If none qualifies but some wait for a grace only, it starts a
// collection. Under mu.
func (a *allocator) reclaim(ci int) {
	waiting := false
	for k := range a.classes {
		if k == ci {
			continue
		}
		for i := 0; i < len(a.classes[k].chunks); {
			id := a.classes[k].chunks[i]
			if a.cells[id].live.Load() != 0 {
				i++
				continue
			}
			if !a.pastGrace(id) {
				waiting = true
				i++
				continue
			}
			a.demote(id) // removes it from the list: i stays
		}
	}
	if len(a.idle) == 0 && waiting {
		a.kickCollect()
	}
}

// kickCollect runs Collect in the background unless one runs: its stop-the-world is the
// grace of every free before it (the gc callback publishes it).
func (a *allocator) kickCollect() {
	collect := Collect
	if collect == nil || !a.opts.FreeThreaded || a.noCollect.Load() || !a.collecting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.collecting.Store(false)
		collect()
		a.collectCalls.Add(1)
	}()
}

// sweep demotes chunks found empty and past their grace demoteSweeps times in a row, and
// releases idle chunks past idleKeep, or idle for idleSweeps sweeps. It returns how many
// chunks have been empty demoteSweeps times in a row but wait for a grace. Under mu.
func (a *allocator) sweep() (waiting int) {
	for k := range a.classes {
		for i := 0; i < len(a.classes[k].chunks); {
			id := a.classes[k].chunks[i]
			c := &a.chunks[id]
			if a.cells[id].live.Load() != 0 {
				c.quiet, c.empty = 0, 0
				i++
				continue
			}
			if c.empty++; !a.pastGrace(id) {
				if c.empty >= demoteSweeps {
					waiting++
				}
				c.quiet = 0
				i++
				continue
			}
			if c.quiet++; c.quiet < demoteSweeps {
				i++
				continue
			}
			a.demote(id)
		}
	}
	kept := a.idle[:0]
	for i, id := range a.idle {
		c := &a.chunks[id]
		c.quiet++
		// The newest are last: the others go after demoteSweeps, these after idleSweeps.
		if len(a.idle)-i > idleKeep && c.quiet >= demoteSweeps || c.quiet >= idleSweeps {
			a.release(id)
			continue
		}
		kept = append(kept, id)
	}
	a.idle = kept
	return waiting
}

// Sweeper starts the sweeper (package py, once the gc callback is registered).
func Sweeper() {
	if a := cur.Load(); a != nil {
		go a.sweeper(time.NewTicker(sweepInterval).C)
	}
}

// sweeper sweeps at each tick, and runs a collection when empty chunks beyond idleKeep
// wait for a grace that no collection has given for collectAfter ticks.
func (a *allocator) sweeper(tick <-chan time.Time) {
	last, still := a.collections.Load(), 0
	for range tick {
		if a.noSweeper.Load() {
			continue
		}
		a.mu.Lock()
		waiting, idle := a.sweep(), len(a.idle)
		a.mu.Unlock()
		if n := a.collections.Load(); n != last {
			last, still = n, 0
			continue
		}
		if still++; waiting > 0 && waiting+idle > idleKeep && still >= collectAfter {
			still = 0
			a.kickCollect()
		}
	}
}

// GCStart is the gc callback's "start": before the collection's stop-the-world.
func GCStart() {
	if a := cur.Load(); a != nil {
		a.gcStart()
	}
}

// GCStop is the gc callback's "stop", after the collection's last start-the-world: every
// free stamped before its start is past a grace.
func GCStop() {
	if a := cur.Load(); a != nil {
		a.gcStop()
	}
}

func (a *allocator) gcStart() { a.started = a.epoch.Add(1) }

func (a *allocator) gcStop() {
	a.safe.Store(a.started)
	a.collections.Add(1)
}

// Bytes is a body in a fused slot. Converted to Python (package py: From, a call's
// argument, a posted value), it becomes a bytes object in place; Release it if it never
// is.
type Bytes struct {
	a     *allocator
	slot  []byte // the slot, from its header
	id    int32  // the chunk's cell
	idx   int32  // the slot's index in the chunk
	n     int    // the body's length
	room  int    // the room asked for (<= the class's capacity)
	class int
	used  atomic.Bool // converted or released
}

// New takes a slot for up to n bytes, or returns nil: the fusion is off, n is under 2
// (Python's singletons) or above MaxCap, or no memory is left (the caller copies as
// before). A body may still be truncated below 2 bytes: its conversion then copies.
func New(n int) *Bytes {
	a := cur.Load()
	if a == nil {
		return nil
	}
	return a.new(n)
}

func (a *allocator) new(n int) *Bytes {
	if n < 2 || n > MaxCap {
		return nil
	}
	ci := ClassFor(n)
	a.mu.Lock()
	id, idx, ok := a.take(ci)
	var mem []byte
	if ok {
		mem = a.chunks[id].mem
	}
	a.mu.Unlock()
	if !ok {
		return nil
	}
	a.takes.Add(1)
	size := a.classes[ci].size
	return &Bytes{a: a, slot: mem[idx*size : (idx+1)*size : (idx+1)*size], id: int32(id), idx: int32(idx),
		n: n, room: n, class: ci}
}

// Buf is the body's memory, from the slot's ob_sval: write the data here.
func (b *Bytes) Buf() []byte {
	h := b.a.opts.Header
	return b.slot[h : h+b.room : h+b.room]
}

// Truncate sets the length to n <= the room taken.
func (b *Bytes) Truncate(n int) { b.n = n }

// Len is the body's length.
func (b *Bytes) Len() int { return b.n }

// Grow moves the body to a slot with room for n bytes (a copy, as a realloc would) and
// gives the old slot back. Nil if there is no memory: b is kept.
func (b *Bytes) Grow(n int) *Bytes {
	nb := b.a.new(n)
	if nb == nil {
		return nil
	}
	copy(nb.Buf(), b.Buf()[:b.n])
	nb.n = b.n
	b.Release()
	b.a.moves.Add(1)
	return nb
}

// Release gives the slot back if the body was never converted.
func (b *Bytes) Release() {
	if b != nil && b.used.CompareAndSwap(false, true) {
		b.a.push(int(b.id), int(b.idx))
	}
}

// Claim is the conversion's: it marks b converted and returns the slot's address (where
// the bytes header goes) and the body's length; false if b was converted or released
// already. The caller adopts the slot, or gives it back with Abandon.
func (b *Bytes) Claim() (slot unsafe.Pointer, n int, ok bool) {
	if !b.used.CompareAndSwap(false, true) {
		return nil, 0, false
	}
	return unsafe.Pointer(&b.slot[0]), b.n, true
}

// Data is the body (valid until it is adopted or given back).
func (b *Bytes) Data() []byte {
	h := b.a.opts.Header
	return b.slot[h : h+b.n : h+b.n]
}

// Slot is the whole slot, from where the bytes header goes: what a stale reader may see
// (py's startup check compares it across the readers' calls).
func (b *Bytes) Slot() []byte { return b.slot }

// Abandon gives back the slot of a claimed b that the conversion did not adopt (it copied
// the body: a body under 2 bytes, which Python makes a singleton, or a subinterpreter).
func (b *Bytes) Abandon() { b.a.push(int(b.id), int(b.idx)) }

// Stats are the allocator's counters.
type Stats struct {
	Takes        int64 // slots taken (New)
	Moves        int64 // Grow moves
	Collections  int64 // collections seen (gc callback)
	CollectCalls int64 // collections the allocator ran (Collect) for a grace
	ClassChanges int64 // chunks assigned a class other than their last one
	Allocs       int64 // chunks made
	Releases     int64 // chunks given back to Go
	Held         int   // chunks held now
	Idle         int   // of which idle
}

// ReadStats returns the counters (zero while the fusion is off).
func ReadStats() Stats {
	a := cur.Load()
	if a == nil {
		return Stats{}
	}
	return a.stats()
}

func (a *allocator) stats() Stats {
	s := Stats{
		Takes: a.takes.Load(), Moves: a.moves.Load(), Collections: a.collections.Load(),
		CollectCalls: a.collectCalls.Load(), ClassChanges: a.classChanges.Load(),
		Allocs: a.allocs.Load(), Releases: a.releases.Load(),
	}
	a.mu.Lock()
	s.Held = len(a.chunks) - len(a.free)
	s.Idle = len(a.idle)
	a.mu.Unlock()
	return s
}
