package asgi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
	"github.com/moriyoshi/pythago/internal/fusion"
)

// fusedecho answers with the request body's length and SHA-256, and whether every chunk
// was a bytes object.
const fusedecho = `
import hashlib
async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            m = await receive()
            if m["type"] == "lifespan.startup":
                await send({"type": "lifespan.startup.complete"})
            else:
                await send({"type": "lifespan.shutdown.complete"})
                return
    h, n, kinds = hashlib.sha256(), 0, True
    while True:
        m = await receive()
        b = m["body"]
        kinds = kinds and type(b) is bytes
        h.update(b)
        n += len(b)
        if not m["more_body"]:
            break
    await send({"type": "http.response.start", "status": 200, "headers": []})
    await send({"type": "http.response.body", "body": b"%d %s %s" % (n, h.hexdigest().encode(), str(kinds).encode())})
`

// liveSlots sums the chunks' live counts: slots taken or holding live objects.
func liveSlots() int64 {
	var n int64
	for id := range fusion.Cells() {
		n += fusion.Chunk(id).Live
	}
	return n
}

// With the fusion on, request bodies (first chunks and receive() chunks, of known and
// unknown length, inline and on the loop thread) are read into fused slots and reach the
// application whole; bodies above the largest class are copied as before. Once the
// requests are done, every slot they took is free again.
func TestFusedBodies(t *testing.T) {
	if !fusion.On() {
		t.Skip("PYTHAGO_TEST_FUSION=1 runs it")
	}
	for _, loopThread := range []bool{false, true} {
		s, err := asgi.New("fusedecho:app", asgi.Options{Workers: 2, LoopThread: loopThread})
		if err != nil {
			t.Fatal(err)
		}
		live := liveSlots()
		for _, c := range []struct {
			size  int
			known bool
		}{
			{1, true}, {100, true}, {4096, true}, {16 << 10, true}, {64 << 10, true}, {200000, true},
			{100, false}, {3000, false}, {5000, false}, {20000, false}, {70000, false}, {200000, false},
		} {
			before := fusion.ReadStats()
			payload := make([]byte, c.size)
			for i := range payload {
				payload[i] = byte(i*7 + c.size)
			}
			sum := sha256.Sum256(payload)
			want := fmt.Sprintf("%d %s True", c.size, hex.EncodeToString(sum[:]))
			r := allocRequest()
			r.Method = "POST"
			r.URL, _ = url.Parse("http://127.0.0.1:8000/upload")
			if c.known {
				r.ContentLength = int64(c.size)
				r.Body = io.NopCloser(bytes.NewReader(payload))
			} else {
				r.ContentLength = -1
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(payload[:c.size/2]), bytes.NewReader(payload[c.size/2:])))
			}
			w := &countWriter{h: http.Header{}}
			s.ServeHTTP(w, r)
			if w.status != 200 || string(w.body) != want {
				t.Fatalf("LoopThread %v, %d bytes (known %v): status %d, %q; want %q", loopThread, c.size, c.known, w.status, w.body, want)
			}
			after := fusion.ReadStats()
			takes, moves := after.Takes-before.Takes, after.Moves-before.Moves
			t.Logf("LoopThread %v, %d bytes (known %v): %d takes, %d moves", loopThread, c.size, c.known, takes, moves)
			switch {
			case c.size >= 2 && (c.size <= fusion.MaxCap || !c.known) && takes == 0:
				t.Errorf("LoopThread %v, %d bytes (known %v): no fused slot taken", loopThread, c.size, c.known)
			case c.known && c.size > fusion.MaxCap && takes != int64((c.size+fusion.MaxCap-1)/fusion.MaxCap):
				// One slot per BodyChunk (64 KiB) chunk, each of the length left.
				t.Errorf("LoopThread %v, %d bytes: %d takes for its 64 KiB chunks", loopThread, c.size, takes)
			case !c.known && c.size > 4<<10 && moves == 0:
				t.Errorf("LoopThread %v, %d bytes of unknown length: no move to a larger class", loopThread, c.size)
			}
		}
		asgi.WaitIdle(s, 10*time.Second)
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for liveSlots() != live && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := liveSlots(); got != live {
			t.Errorf("LoopThread %v: %d slots live after the requests, %d before", loopThread, got, live)
		}
	}
}
