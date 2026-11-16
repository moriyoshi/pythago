package asgi

import "testing"

// A recycled request's lanes are reset with it: requests that each take the same amount
// keep reusing the same memory instead of growing their lanes, and a new request's first
// take starts at the beginning of its lane.
func TestRequestLanesReset(t *testing.T) {
	var first *byte
	for i := range 1000 {
		sc := requests.Get()
		q := &sc.Root
		if q.mem == nil { // as newRequest makes one
			q.sc, q.kick, q.mem = sc, make(chan struct{}, 1), new(requestMem)
			q.mem.bytes.SetNoZero()
		}
		b := q.mem.bytes.Take(100)
		q.mem.pairs.Take(5)
		if i == 0 {
			first = &b[0]
		} else if &b[0] != first && q.mem.bytes.Cap() > 1000 {
			t.Fatalf("request %d: its bytes lane holds %d bytes after 100 per request; want it reset", i, q.mem.bytes.Cap())
		}
		sc.Release()
	}
	sc := requests.Get()
	defer sc.Release()
	if n := sc.Root.mem.pairs.Cap(); n > 64 {
		t.Errorf("after 1000 requests of 5 pairs the pairs lane holds %d; want it reset", n)
	}
}
