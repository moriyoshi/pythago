package arena

import (
	"strings"
	"sync"
	"testing"
)

// Goroutines taking from one SharedLane at once, across chunk changes, never get
// overlapping memory, and every take is zeroed, also in lifetimes after a Reset. Each
// goroutine fills what it took with its own id and checks it once all have finished.
func TestSharedLaneConcurrentTakes(t *testing.T) {
	var l SharedLane[int]
	const goroutines, takes = 8, 200
	for life := range 30 {
		var wg sync.WaitGroup
		got := make([][][]int, goroutines)
		bad := make(chan string, goroutines)
		for g := range goroutines {
			wg.Go(func() {
				for k := range takes {
					s := l.Take(1 + (k+g)%7)
					for i := range s {
						if s[i] != 0 {
							bad <- "a take was not zeroed"
							return
						}
						s[i] = g + 1
					}
					got[g] = append(got[g], s)
				}
			})
		}
		wg.Wait()
		close(bad)
		for msg := range bad {
			t.Fatalf("lifetime %d: %s", life, msg)
		}
		for g, ss := range got {
			for _, s := range ss {
				for _, v := range s {
					if v != g+1 {
						t.Fatalf("lifetime %d: goroutine %d's take holds %d: two takes overlapped", life, g, v)
					}
				}
			}
		}
		l.Reset()
	}
}

// Goroutines that start together on a new lane race to start its chunks: every take of a
// lifetime that grows the lane from nothing, many times over, still gets memory of its
// own.
func TestSharedLaneGrowthRace(t *testing.T) {
	const goroutines, takes = 8, 64
	for life := range 2000 {
		var l SharedLane[int32]
		var start, done sync.WaitGroup
		start.Add(1)
		got := make([][][]int32, goroutines)
		for g := range goroutines {
			done.Go(func() {
				start.Wait()
				for k := range takes {
					s := l.Take(1 + (k+g)%3)
					for i := range s {
						s[i] = int32(g + 1)
					}
					got[g] = append(got[g], s)
				}
			})
		}
		start.Done()
		done.Wait()
		for g, ss := range got {
			for _, s := range ss {
				for _, v := range s {
					if v != int32(g+1) {
						t.Fatalf("lifetime %d: goroutine %d's take holds %d: two takes overlapped", life, g, v)
					}
				}
			}
		}
	}
}

// A SharedLane keeps what a lifetime took for the next (a lifetime of the same shape
// then takes from one chunk without allocating), and drops a chunk past its bound.
func TestSharedLaneKeepsAndTrims(t *testing.T) {
	var l SharedLane[[2]string]
	for range 3 {
		l.Take(30)
		l.Take(70)
		l.Reset()
	}
	if l.Cap() < 100 {
		t.Fatalf("after lifetimes of 100 the lane kept %d; want at least 100", l.Cap())
	}
	if n := testing.AllocsPerRun(100, func() { l.Take(30); l.Take(70); l.Reset() }); n != 0 {
		t.Errorf("a lifetime of the same shape allocates %v times; want 0", n)
	}
	l.Take(Keep/32 + 1)
	l.Reset()
	if l.Cap() != 0 {
		t.Errorf("after a lifetime past Keep the lane kept %d pairs; want none", l.Cap())
	}
}

// A scope's root is reset when its last reference is released, and not before;
// releasing more often than retaining panics; a detached scope is never reset.
func TestScopeReferences(t *testing.T) {
	type root struct{ id, resets int }
	resets := 0
	p := NewPool(func(r *root) { resets++; r.id = 0 })
	s := p.Get()
	s.Root.id = 7
	s.Retain()
	s.Release()
	if s.Root.id != 7 || resets != 0 {
		t.Fatalf("the root was reset with a reference left (id %d, resets %d)", s.Root.id, resets)
	}
	s.Release()
	if s.Root.id != 0 || resets != 1 {
		t.Fatalf("after the last release: id %d, resets %d; want 0 and 1", s.Root.id, resets)
	}
	msg := func() (msg string) {
		defer func() { msg, _ = recover().(string) }()
		s.Release()
		return ""
	}()
	if !strings.Contains(msg, "released more often") {
		t.Errorf("an extra Release: %q; want a panic", msg)
	}
	d := p.Get()
	d.Root.id = 9
	d.Detach()
	d.Release()
	if d.Root.id != 9 || resets != 1 {
		t.Errorf("a detached scope was reset (id %d, resets %d)", d.Root.id, resets)
	}
}

// TryRetain adds a reference only to the lifetime it names: a handle of a lifetime that
// ended finds the scope serving another one (reused from the pool) and gets nothing,
// leaving that lifetime's references as they were; a handle of the current lifetime
// gets a reference; a scope whose last reference is gone gives none.
func TestTryRetainLifetime(t *testing.T) {
	type root struct{ resets int }
	p := NewPool(func(r *root) { r.resets++ })
	s := p.Get()
	old := s.ID()
	s.Release()
	if s.TryRetain(old) {
		t.Fatal("TryRetain succeeded on a scope with no reference")
	}
	var s2 *Scope[root]
	for range 100 { // the pool may hand out another scope: find this one again
		if s2 = p.Get(); s2 == s {
			break
		}
	}
	if s2 != s {
		t.Skip("the pool never returned the released scope")
	}
	if s.ID() == old {
		t.Fatalf("a new lifetime kept the old one's ID %d", old)
	}
	resets := s.Root.resets
	if s.TryRetain(old) {
		t.Fatal("TryRetain with an ended lifetime's ID took a reference to the new lifetime")
	}
	if !s.TryRetain(s.ID()) {
		t.Fatal("TryRetain with the current lifetime's ID failed")
	}
	s.Release()
	if s.Root.resets != resets {
		t.Fatal("the failed TryRetain's release ended the new lifetime")
	}
	s.Release() // its last reference
	if s.Root.resets != resets+1 {
		t.Errorf("after the last release the root was reset %d times; want %d", s.Root.resets, resets+1)
	}
}

// Extend grows the lane's last take in place, keeping its contents; it refuses once
// another take followed, or when the chunk has no room, and then nothing changes.
func TestSharedLaneExtend(t *testing.T) {
	var l SharedLane[byte]
	l.Take(1024) // a chunk with room
	l.Reset()
	a := l.Take(10)
	copy(a, "0123456789")
	b, ok := l.Extend(a, 6)
	if !ok || len(b) != 16 || string(b[:10]) != "0123456789" || &b[0] != &a[0] {
		t.Fatalf("Extend of the last take: %v, %q", ok, b)
	}
	c := l.Take(4)
	if &c[0] == &b[10] {
		t.Fatal("a take after an extension overlaps it")
	}
	if _, ok := l.Extend(b, 1); ok {
		t.Error("Extend of a take that is no longer the last one succeeded")
	}
	if _, ok := l.Extend(c, 1<<20); ok {
		t.Error("Extend past the chunk's room succeeded")
	}
}

// A lane with SetKeep keeps a chunk past Keep while its lifetimes use more than Keep,
// and drops it after keepIdle lifetimes in a row that did not.
func TestSharedLaneKeepDecay(t *testing.T) {
	var l SharedLane[byte]
	l.SetKeep(256 << 10)
	big := func() { l.Take(Keep + 1000); l.Reset() }
	big()
	big()
	if l.Cap() <= Keep {
		t.Fatalf("a chunk past Keep (%d) was not kept while lifetimes use it", l.Cap())
	}
	if n := testing.AllocsPerRun(50, big); n != 0 {
		t.Errorf("a lifetime using the kept chunk allocates %v times", n)
	}
	for range keepIdle - 1 {
		l.Take(100)
		l.Reset()
	}
	if l.Cap() <= Keep {
		t.Fatalf("the chunk was dropped after only %d small lifetimes", keepIdle-1)
	}
	l.Take(100)
	l.Reset()
	if l.Cap() != 0 {
		t.Errorf("after %d small lifetimes the lane still keeps %d", keepIdle, l.Cap())
	}
}
