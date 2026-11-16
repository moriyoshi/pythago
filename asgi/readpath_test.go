package asgi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
)

// uploadEcho answers with the body's length.
const uploadecho = `
async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            m = await receive()
            if m["type"] == "lifespan.startup":
                await send({"type": "lifespan.startup.complete"})
            else:
                await send({"type": "lifespan.shutdown.complete"})
                return
    n = 0
    while True:
        m = await receive()
        n += len(m["body"])
        if not m["more_body"]:
            break
    await send({"type": "http.response.start", "status": 200, "headers": []})
    await send({"type": "http.response.body", "body": b"%d" % n})
`

type countWriter struct {
	h      http.Header
	status int
	body   []byte
}

func (w *countWriter) Header() http.Header         { return w.h }
func (w *countWriter) WriteHeader(status int)      { w.status = status }
func (w *countWriter) Write(b []byte) (int, error) { w.body = append(w.body, b...); return len(b), nil }
func (w *countWriter) Flush()                      {}

// uploadBytes returns the Go bytes allocated per upload of size bytes through ServeHTTP
// (no network), after a warm-up.
func uploadBytes(t *testing.T, loopThread bool, size int) uint64 {
	s, err := asgi.New("uploadecho:app", asgi.Options{Workers: 1, LoopThread: loopThread})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	payload := bytes.Repeat([]byte("x"), size)
	want := fmt.Sprint(size)
	r := allocRequest()
	r.Method = "POST"
	r.URL, _ = url.Parse("http://127.0.0.1:8000/upload")
	r.ContentLength = int64(size)
	rd := bytes.NewReader(payload)
	w := &countWriter{h: http.Header{}}
	serve := func() {
		rd.Reset(payload)
		r.Body = io.NopCloser(rd)
		clear(w.h)
		w.status, w.body = 0, w.body[:0]
		s.ServeHTTP(w, r)
		if w.status != 200 || string(w.body) != want {
			t.Fatalf("status %d, body %q; want 200 and %q", w.status, w.body, want)
		}
	}
	for range 50 {
		serve()
	}
	asgi.WaitIdle(s, 10*time.Second)
	var before, after runtime.MemStats
	const n = 300
	runtime.ReadMemStats(&before)
	for range n {
		serve()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / n
}

// An inline 64 KiB upload (a first chunk of BodyChunk) grows in the request's bytes
// lane, in place, and the lane keeps the chunk for the next request: the body costs no
// Go allocation. It cost about 258 KB per request when the lane dropped its chunk every
// time (its growth steps passed the default bound).
func TestInlineUpload64KiBNotAllocated(t *testing.T) {
	if os.Getenv("PYTHAGO_ASGI_LOOP_THREAD") == "1" {
		t.Skip("inline only")
	}
	per := uploadBytes(t, false, 64<<10)
	t.Logf("Go bytes allocated per inline 64 KiB upload: %d", per)
	if per > 16<<10 {
		t.Errorf("an inline 64 KiB upload allocates %d bytes; its first chunk is not kept", per)
	}
}

// A posted (LoopThread) 100-byte upload reads its first chunk into a buffer of its
// declared length, not into a 4 KiB one.
func TestLoopThreadSmallUploadSized(t *testing.T) {
	per := uploadBytes(t, true, 100)
	t.Logf("Go bytes allocated per posted 100 B upload: %d", per)
	if per > 3<<10 {
		t.Errorf("a posted 100-byte upload allocates %d bytes; its first chunk's buffer is not sized to the body", per)
	}
}
