package asgi

import (
	"strings"
	"testing"

	"github.com/moriyoshi/pythago/internal/lease"
)

// A receive() chunk read while the last one is still lent (not yet converted) gets
// memory of its own; once the lease is released, the buffer serves the next chunk.
func TestReceiveBufferBusy(t *testing.T) {
	sc := requests.Get()
	q := &sc.Root
	if q.mem == nil {
		q.sc, q.kick, q.mem = sc, make(chan struct{}, 1), new(requestMem)
		q.mem.bytes.SetNoZero()
	}
	defer sc.Release()
	read := func(s string) lease.Bytes {
		v, _, err := q.readPosted(strings.NewReader(s), 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		if l, ok := v.(lease.Bytes); ok {
			return l
		}
		return nil
	}
	first := read("first chunk")
	if first == nil {
		t.Fatal("the first chunk was not lent from the receive buffer")
	}
	second, _, _ := q.readPosted(strings.NewReader("second!!!!!"), 64<<10)
	if string(first.LeasedBytes()) != "first chunk" {
		t.Fatalf("a second read while the first chunk was lent overwrote it: %q", first.LeasedBytes())
	}
	if _, lent := second.(lease.Bytes); lent {
		t.Error("a second chunk was lent from the busy receive buffer")
	}
	first.ReleaseLease()
	third := read("third")
	if third == nil {
		t.Error("after the release, the receive buffer is still busy")
	} else {
		third.ReleaseLease()
	}
}
