package asgi

import (
	"io"
	"strings"
	"testing"

	"github.com/moriyoshi/pythago/internal/arena"
)

// stalledBody returns its data, then reports EOF: a client that declared more than it
// sent.
type stalledBody struct{ r io.Reader }

func (b stalledBody) Read(p []byte) (int, error) { return b.r.Read(p) }

// A first chunk's memory follows what the client sent, not what it declared: a request
// that declares 64 KiB and sends 10 bytes takes the first 4 KiB step only, from the heap
// or the request's lane alike.
func TestFirstChunkFollowsData(t *testing.T) {
	var lane arena.SharedLane[byte]
	for _, l := range []*arena.SharedLane[byte]{nil, &lane} {
		body, more, err := readFirstChunk(l, stalledBody{strings.NewReader("ten bytes!")}, 64<<10, 64<<10)
		if err != nil || string(body) != "ten bytes!" || more {
			t.Fatalf("lane %v: %q, more %v, %v", l != nil, body, more, err)
		}
		if cap(body) > 4<<10 {
			t.Errorf("lane %v: 10 bytes of a declared 64 KiB took %d bytes", l != nil, cap(body))
		}
	}
	if lane.Cap() > 4<<10 {
		t.Errorf("the lane holds %d bytes after 10 were read", lane.Cap())
	}
}
