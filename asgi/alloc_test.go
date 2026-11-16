package asgi_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
)

// allocapp answers like the benchmarks' hello application: a fixed head and body, all
// in the first call (inline, nothing awaits).
const allocapp = `
HEAD = {"type": "http.response.start", "status": 200,
        "headers": [(b"content-type", b"text/plain"), (b"content-length", b"13")]}
BODY = {"type": "http.response.body", "body": b"Hello, world!"}

async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            m = await receive()
            if m["type"] == "lifespan.startup":
                await send({"type": "lifespan.startup.complete"})
            else:
                await send({"type": "lifespan.shutdown.complete"})
                return
    await send(HEAD)
    await send(BODY)
`

// discardWriter is an http.ResponseWriter that keeps nothing but the status, so that
// what a request allocates is the server's (Header's map is cleared and reused).
type discardWriter struct {
	h      http.Header
	status int
	n      int
}

func (w *discardWriter) Header() http.Header         { return w.h }
func (w *discardWriter) WriteHeader(status int)      { w.status = status }
func (w *discardWriter) Write(b []byte) (int, error) { w.n += len(b); return len(b), nil }

// allocRequest is a GET as net/http hands it to a handler: a client and a local address,
// a few headers, no body.
func allocRequest() *http.Request {
	r, _ := http.NewRequest("GET", "http://127.0.0.1:8000/hello", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("User-Agent", "alloc-test")
	r.Header.Set("Accept", "*/*")
	r.Host = "127.0.0.1:8000"
	r.Body = http.NoBody
	local := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8000}
	return r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, local))
}

func serveAlloc(t testing.TB, s *asgi.Server, r *http.Request, w *discardWriter) {
	clear(w.h)
	w.status, w.n = 0, 0
	s.ServeHTTP(w, r)
	if w.status != 200 || w.n != 13 {
		t.Fatalf("status %d, %d body bytes; want 200 and 13", w.status, w.n)
	}
}

// maxInlineAllocs is what one inline request allocates in Go (ServeHTTP to the response,
// the application's Python aside), as measured: 2 for the response fields (one string
// and the header values), the rest boxing the arguments (raw_path, path, packed headers,
// h, the client's address); py's dispatch of respond allocates nothing since its call
// scopes (it was 2, its argument slots, before).
// It was 37 before requests were recycled (request, kick channel, action queue, header
// pairs, body copy), the arguments built in the holder's reused array instead of a scope
// tuple and a slice per request, and the server address cached per worker without
// formatting it.
const maxInlineAllocs = 7

// TestInlineAllocs pins the Go allocations of one inline request.
func TestInlineAllocs(t *testing.T) {
	s, err := asgi.New("allocapp:app", asgi.Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	r := allocRequest()
	w := &discardWriter{h: http.Header{}}
	for range 100 { // warm: the pool, the address cache, map buckets
		serveAlloc(t, s, r, w)
	}
	if err := asgi.WaitIdle(s, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	got := testing.AllocsPerRun(2000, func() { serveAlloc(t, s, r, w) })
	t.Logf("allocations per inline request: %v", got)
	if got > maxInlineAllocs {
		t.Errorf("an inline request allocates %v times, want at most %d (go test -run TestInlineAllocs -memprofile m -memprofilerate 1 shows where)",
			got, maxInlineAllocs)
	}
}

func BenchmarkInlineRequest(b *testing.B) {
	s, err := asgi.New("allocapp:app", asgi.Options{Workers: 1})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close(context.Background())
	r := allocRequest()
	w := &discardWriter{h: http.Header{}}
	serveAlloc(b, s, r, w)
	asgi.WaitIdle(s, 10*time.Second)
	b.ReportAllocs()
	for b.Loop() {
		serveAlloc(b, s, r, w)
	}
}
