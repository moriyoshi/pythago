package asgi_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

// Request bodies around the first chunk's limits (the 4 KiB first buffer, the 64 KiB
// chunk) reach the application whole, whether their length is declared or not (chunked):
// a declared body that fits one chunk is read to its length and ends there, a longer one
// continues in later chunks.
func TestFirstChunkSizes(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for _, size := range []int{1, 100, 4095, 4096, 4097, 16384, 65535, 65536, 65537, 200000} {
		for _, declared := range []bool{true, false} {
			payload := make([]byte, size)
			rand.Read(payload)
			var r io.Reader = bytes.NewReader(payload)
			if !declared {
				r = io.MultiReader(r) // no length: sent chunked
			}
			req, _ := http.NewRequest("POST", ts.URL+"/echo", r)
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if got, want := resp.Header.Get("X-Body-Sha256"), fmt.Sprintf("%x", sha256.Sum256(payload)); got != want {
				t.Errorf("%d bytes (declared %v): the application saw sha256 %s, want %s", size, declared, got, want)
			}
		}
	}
}

// An inline request's first body chunk is read into the request's memory, which the
// pooled request keeps: a 16 KiB upload allocates nothing for its body in Go. Measured
// with the client in the same process (2026-11-20, 3.14t): about 24 KB per request for
// both sides; 58 KB when the chunk's buffer was allocated (and grown as it filled).
func TestUploadBodyNotAllocated(t *testing.T) {
	if os.Getenv("PYTHAGO_ASGI_LOOP_THREAD") == "1" {
		t.Skip("posted requests read their first chunk into memory of their own")
	}
	payload := bytes.Repeat([]byte("x"), 16<<10)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	post := func() {
		resp, err := ts.Client().Post(ts.URL+"/echo", "application/octet-stream", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	for range 20 {
		post()
	}
	// Server and client share the process: measure the client alone the same way and
	// compare. Each POST's client side sends the same payload either way.
	var before, after runtime.MemStats
	const n = 200
	runtime.ReadMemStats(&before)
	for range n {
		post()
	}
	runtime.ReadMemStats(&after)
	perRequest := (after.TotalAlloc - before.TotalAlloc) / n
	t.Logf("bytes allocated per 16 KiB upload (client and server): %d", perRequest)
	if perRequest > 40<<10 {
		t.Errorf("a 16 KiB upload allocates %d bytes per request (client and server); its body's buffer is allocated", perRequest)
	}
}
