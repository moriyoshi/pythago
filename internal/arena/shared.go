package arena

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// A SharedLane is a Lane that several goroutines may take from at once: the goroutines
// that share a request (its handler, the thread running the application, Go functions
// it calls). A take is one atomic add on the current chunk; only a take that does not
// fit locks, to start a new chunk. Reset is not concurrent with takes: it runs once the
// lifetime is over (a Scope's last Release). The zero SharedLane is ready to use.
//
// Memory ordering: a chunk is published by cur.Store after its buf is made (zeroed) and
// its used counter set, so a taker that loads it (cur.Load, which synchronizes with the
// Store) sees both. Within a chunk, every take adds its n to used atomically, so the
// intervals [end-n, end) of takes that fit (end <= len(buf)) are disjoint; a take that
// overflows only wastes the rest of that chunk, and used only grows during a lifetime.
// A taker may still add to a chunk that another has retired (it loaded cur before the
// Store of the next): what it gets is still a disjoint interval of memory that stays
// valid, since retired chunks are kept in full until Reset. Reset happens after every
// take of the lifetime: the takers hold the scope's references, and the last Release's
// atomic decrement comes after each of theirs.
type SharedLane[T any] struct {
	cur    atomic.Pointer[sharedChunk[T]]
	mu     sync.Mutex        // starting a chunk
	full   []*sharedChunk[T] // chunks retired this lifetime, still in use
	keep   int               // elements kept between lifetimes: Keep bytes' worth
	most   int               // SetKeep's: kept while lifetimes use more than keep
	idle   int               // lifetimes in a row that used at most keep
	noZero bool
}

// keepIdle is how many lifetimes in a row may use at most the default bound before a
// lane drops a chunk kept past it (SetKeep).
const keepIdle = 16

type sharedChunk[T any] struct {
	buf  []T // never changes once published
	used atomic.Int64
}

// SetNoZero makes Reset leave what was taken as it was: every taker overwrites what it
// takes. Call it before the lane is used.
func (l *SharedLane[T]) SetNoZero() { l.noZero = true }

// SetKeep lets the lane keep a chunk of up to n elements, past Keep bytes' worth, while
// its lifetimes use more than Keep's worth: one that takes a known large amount every
// time (a request's first body chunk) then reuses its chunk instead of making it again.
// After keepIdle lifetimes in a row that used no more than Keep's worth, the chunk is
// dropped. Call it between lifetimes.
func (l *SharedLane[T]) SetKeep(n int) { l.most = n }

// Take returns n elements, as a full slice (an append copies), valid until Reset. They
// are zeroed unless SetNoZero was called.
func (l *SharedLane[T]) Take(n int) []T {
	if c := l.cur.Load(); c != nil {
		if end := c.used.Add(int64(n)); end <= int64(len(c.buf)) {
			return c.buf[end-int64(n) : end : end]
		}
	}
	return l.grow(n)
}

// Extend returns s, the lane's last take, extended by n elements in place (zeroed unless
// SetNoZero), or false if it cannot be: s is not the current chunk's last take any more
// (another take followed it), or the chunk has no room. Like Take, it is lock-free.
func (l *SharedLane[T]) Extend(s []T, n int) ([]T, bool) {
	c := l.cur.Load()
	if c == nil || len(s) == 0 {
		return nil, false
	}
	start := int64(uintptr(unsafe.Pointer(unsafe.SliceData(s)))-uintptr(unsafe.Pointer(unsafe.SliceData(c.buf)))) / int64(unsafe.Sizeof(*new(T)))
	end := start + int64(len(s))
	if start < 0 || end > int64(len(c.buf)) || unsafe.SliceData(c.buf[start:end]) != unsafe.SliceData(s) ||
		end+int64(n) > int64(len(c.buf)) || !c.used.CompareAndSwap(end, end+int64(n)) {
		return nil, false
	}
	return c.buf[start : end+int64(n) : end+int64(n)], true
}

// grow takes n from a new chunk, unless one that another taker started meanwhile fits.
func (l *SharedLane[T]) grow(n int) []T {
	l.mu.Lock()
	defer l.mu.Unlock()
	size := max(n, 8)
	if c := l.cur.Load(); c != nil {
		if end := c.used.Add(int64(n)); end <= int64(len(c.buf)) {
			return c.buf[end-int64(n) : end : end]
		}
		l.full = append(l.full, c)
		size = max(n, 2*len(c.buf))
	}
	c := &sharedChunk[T]{buf: make([]T, size)}
	c.used.Store(int64(n))
	l.cur.Store(c)
	return c.buf[:n:n]
}

// Reset ends the lifetime, once no one takes any more: what was taken is zeroed (unless
// SetNoZero) and may be handed out again; the lane drops the retired chunks and, past
// Keep bytes, the current one.
func (l *SharedLane[T]) Reset() {
	c := l.cur.Load()
	if c == nil {
		return
	}
	if l.keep == 0 {
		l.keep = Keep / max(int(unsafe.Sizeof(*new(T))), 1)
	}
	used := int(min(c.used.Load(), int64(len(c.buf))))
	if len(c.buf) > l.keep && used <= l.keep {
		l.idle++
	} else {
		l.idle = 0
	}
	if len(c.buf) > max(l.keep, l.most) || len(c.buf) > l.keep && l.idle >= keepIdle {
		l.cur.Store(nil)
		l.idle = 0
	} else {
		if !l.noZero {
			clear(c.buf[:min(c.used.Load(), int64(len(c.buf)))])
		}
		c.used.Store(0)
	}
	clear(l.full)
	l.full = l.full[:0]
}

// Cap returns the size of the lane's current chunk (for tests).
func (l *SharedLane[T]) Cap() int {
	if c := l.cur.Load(); c != nil {
		return len(c.buf)
	}
	return 0
}

// A Pool hands out values of type T with a reference count each, for a lifetime that
// several goroutines share (a server's request): Get returns one with a reference; the
// holders Retain and Release theirs, and the last Release resets the value (the pool's
// reset function, which also resets the lanes the value holds) and reuses it.
type Pool[T any] struct {
	pool  sync.Pool
	reset func(*T)
}

// A Scope is a pooled value (Root) and its reference count.
type Scope[T any] struct {
	Root     T
	refs     atomic.Int32
	detached atomic.Bool
	id       atomic.Int64 // this lifetime's (ID)
	pool     *Pool[T]
}

// scopeIDs numbers lifetimes, over all pools: an ID is never reused.
var scopeIDs atomic.Int64

// NewPool returns a pool whose values reset resets for reuse (nil: zeroed).
func NewPool[T any](reset func(*T)) *Pool[T] { return &Pool[T]{reset: reset} }

// Get returns a scope with one reference.
func (p *Pool[T]) Get() *Scope[T] {
	s, _ := p.pool.Get().(*Scope[T])
	if s == nil {
		s = &Scope[T]{pool: p}
	}
	s.id.Store(scopeIDs.Add(1)) // before refs: TryRetain checks the ID after its add
	s.detached.Store(false)
	s.refs.Store(1)
	return s
}

// ID returns the scope's lifetime: a number Get gives no other lifetime of any scope,
// for handles that must not outlive it (a request's, which a stale holder may still
// present after the scope serves another lifetime).
func (s *Scope[T]) ID() int64 { return s.id.Load() }

// TryRetain adds a reference if the scope is still serving the lifetime id, for a holder
// that found the scope without holding a reference (a handle's lookup): it fails if that
// lifetime has ended, even if the scope already serves another. The reference is added
// before the ID is checked, so the lifetime cannot end in between; a holder that added
// one to a later lifetime drops it again (possibly ending that lifetime, if it was the
// last).
func (s *Scope[T]) TryRetain(id int64) bool {
	for {
		n := s.refs.Load()
		if n <= 0 {
			return false
		}
		if s.refs.CompareAndSwap(n, n+1) {
			break
		}
	}
	if s.id.Load() != id {
		s.Release()
		return false
	}
	return true
}

// Retain adds a reference. The caller must hold one already, or know that the lifetime
// has not ended (a request still registered, under the lock its last holder takes to
// unregister it).
func (s *Scope[T]) Retain() { s.refs.Add(1) }

// Release drops a reference. The last one ends the lifetime: the root is reset and the
// scope reused, unless it was detached. Releasing more often than retaining panics.
func (s *Scope[T]) Release() {
	switch n := s.refs.Add(-1); {
	case n > 0:
		return
	case n < 0:
		panic("arena: Scope released more often than retained")
	}
	if s.detached.Load() {
		return
	}
	if s.pool.reset != nil {
		s.pool.reset(&s.Root)
	} else {
		s.Root = *new(T)
	}
	s.pool.pool.Put(s)
}

// Detach makes the scope never reused: when its last reference is released, the garbage
// collector reclaims it. For a lifetime that something outside the reference count may
// outlive (a WebSocket's reader).
func (s *Scope[T]) Detach() { s.detached.Store(true) }
