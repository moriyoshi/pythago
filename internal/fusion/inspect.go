package fusion

import "unsafe"

// What tests (package py's and asgi's) inspect and drive. Nothing here is on a hot path.

// ChunkInfo describes a chunk cell.
type ChunkInfo struct {
	Held   bool    // the cell holds a chunk (registered and pinned)
	Base   uintptr // its address
	Class  int     // its class, or -1: idle (or none)
	Carved int     // slots handed out since assigned
	Slots  int     // its slots
	Live   int64   // slots taken or holding a live object
	Stamp  uint64  // the epoch of its last free by Python (0: none since assigned)
	Free   bool    // its free stack is not empty
	Mem    []byte  // the chunk (nil if none)
}

// Chunk describes cell id (zero if the fusion is off or id is out of range).
func Chunk(id int) ChunkInfo {
	a := cur.Load()
	if a == nil || id < 0 || id >= len(a.chunks) {
		return ChunkInfo{Class: noClass}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := &a.chunks[id]
	st := &a.cells[id]
	return ChunkInfo{Held: c.mem != nil, Base: st.base.Load(), Class: c.class, Carved: c.carved,
		Slots: c.nslots, Live: st.live.Load(), Stamp: st.stamp.Load(), Free: uint32(st.head.Load()) != 0, Mem: c.mem}
}

// Cells is the number of chunk cells (the cap in chunks).
func Cells() int {
	if a := cur.Load(); a != nil {
		return len(a.chunks)
	}
	return 0
}

// Locate returns the cell whose chunk holds address p, as Go knows it, and p's offset in
// the chunk.
func Locate(p uintptr) (id, off int, ok bool) {
	a := cur.Load()
	if a == nil {
		return 0, 0, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, c := range a.chunks {
		if c.mem != nil {
			if b := uintptr(unsafe.Pointer(&c.mem[0])); p-b < ChunkSize {
				return i, int(p - b), true
			}
		}
	}
	return 0, 0, false
}

// Addr is the slot's address: the bytes object's, once adopted.
func (b *Bytes) Addr() uintptr { return uintptr(unsafe.Pointer(&b.slot[0])) }

// Cell is the slot's chunk cell.
func (b *Bytes) Cell() int { return int(b.id) }

// SetNoCollect stops the allocator from running Collect for a grace (true) or lets it
// again; it returns the previous setting.
func SetNoCollect(v bool) bool {
	if a := cur.Load(); a != nil {
		return a.noCollect.Swap(v)
	}
	return false
}

// PauseSweeper makes the sweeper skip its sweeps (true) or run them again; it returns the
// previous setting. Tests that count sweeps or collections pause it.
func PauseSweeper(v bool) bool {
	if a := cur.Load(); a != nil {
		return a.noSweeper.Swap(v)
	}
	return false
}

// Sweep runs a sweep, as the sweeper does at each tick.
func Sweep() {
	if a := cur.Load(); a != nil {
		a.mu.Lock()
		a.sweep()
		a.mu.Unlock()
	}
}

// DemoteSweeps and IdleSweeps are the decay's sweep counts, for tests that wait them out.
const (
	DemoteSweeps = demoteSweeps
	IdleSweeps   = idleSweeps
	IdleKeep     = idleKeep
)
