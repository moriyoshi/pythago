package wsgi_test

import (
	"net/http"
	"testing"
)

// discardWriter is an http.ResponseWriter that keeps nothing but the status and the
// body's length, so that what a request allocates is the server's (Header's map is
// cleared and reused).
type discardWriter struct {
	h      http.Header
	status int
	n      int
}

func (w *discardWriter) Header() http.Header         { return w.h }
func (w *discardWriter) WriteHeader(status int)      { w.status = status }
func (w *discardWriter) Write(b []byte) (int, error) { w.n += len(b); return len(b), nil }
func (w *discardWriter) Flush()                      {}

// maxRequestAllocs is what one request of testapp's /hello allocates in Go (ServeHTTP
// to the response, the application's Python aside), as measured on 2026-11-20 (3.14t,
// memprofile):
//   - 3 result Objects (py.own);
//   - 2 for the response head (the header map's value slices);
//   - 1 string copy of the packed headers, which the pairs' strings share;
//   - the request's sync.Map entry, py.Do's closure and the boxing of serve's arguments;
//   - 1 in write (UnsafeBytes) and, every other request, one in Int64.
//
// It was 21 before the request was recycled in an arena scope (2026-11-20): the request,
// the environ buffer and its HTTP_ names, the header pairs, their strings.Split fields
// and lower-cased names, and the first pending copy of the body came from the heap.
const maxRequestAllocs = 12

// TestRequestAllocs pins the Go allocations of one request.
func TestRequestAllocs(t *testing.T) {
	r, _ := http.NewRequest("GET", "http://127.0.0.1:8000/hello", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("User-Agent", "alloc-test")
	r.Header.Set("Accept", "*/*")
	r.Host = "127.0.0.1:8000"
	r.Body = http.NoBody
	w := &discardWriter{h: http.Header{}}
	serve := func() {
		clear(w.h)
		w.status, w.n = 0, 0
		srv.ServeHTTP(w, r)
		if w.status != 200 || w.n != 13 {
			t.Fatalf("status %d, %d body bytes; want 200 and 13", w.status, w.n)
		}
	}
	for range 100 { // warm: the pool, the lanes
		serve()
	}
	got := testing.AllocsPerRun(2000, serve)
	t.Logf("allocations per request: %v", got)
	if got > maxRequestAllocs {
		t.Errorf("a request allocates %v times, want at most %d (go test -run TestRequestAllocs -memprofile m -memprofilerate 1 shows where)", got, maxRequestAllocs)
	}
}
