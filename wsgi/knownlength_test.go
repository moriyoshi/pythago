package wsgi_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/moriyoshi/pythago/wsgi"
)

// pattern is testapp's pattern(): chunk i of a /held response.
func pattern(seed, i, n int) []byte {
	unit := sha256.Sum256(fmt.Appendf(nil, "%d-%d", seed, i))
	return bytes.Repeat(unit[:], n/32+1)[:n]
}

func heldBody(seed int, sizes string) []byte {
	var out []byte
	for i, s := range strings.Split(sizes, ",") {
		n, _ := strconv.Atoi(s)
		out = append(out, pattern(seed, i, n)...)
	}
	return out
}

// TestKnownLengthBodyAfterReturn: a list or tuple body is written after the application
// returns, from its bytes objects, with no write while Python's call is in progress: such
// a write detaches the thread state mid-request and needs the GIL and an admission slot
// back (a second acquisition per request, which sustains the GIL/P convoy, journal
// 2026-11-18). The chunks arrive whole and in order, though the application's objects are
// freed when its call returns, and the references Go held are released.
func TestKnownLengthBodyAfterReturn(t *testing.T) {
	plain := httptest.NewServer(srv)
	defer plain.Close()
	h2 := httptest.NewUnstartedServer(srv)
	h2.EnableHTTP2 = true
	h2.StartTLS()
	defer h2.Close()
	chunk := bytes.Repeat(func() []byte {
		b := make([]byte, 256)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}(), 256)
	known := []struct{ path, sizes string }{
		{"/held?sizes=65536", "65536"},
		{"/held?sizes=16385", "16385"},
		// copied, held, copied (gathered), held, copied: in order
		{"/held?sizes=100,20000,5000,3000,70000,1,4096", "100,20000,5000,3000,70000,1,4096"},
		{"/held?sizes=16384,4096,4096", "16384,4096,4096"},
		{"/list?n=4", ""}, // one object four times
	}

	count := wsgi.CountDetachedWrites()
	for _, s := range []*httptest.Server{plain, h2} {
		c := s.Client()
		for _, k := range known {
			want := heldBody(0, k.sizes)
			if k.sizes == "" {
				want = bytes.Repeat(chunk, 4)
			}
			resp, err := c.Get(s.URL + k.path)
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || !bytes.Equal(b, want) || resp.ContentLength != int64(len(want)) {
				t.Errorf("%s %s: %d bytes (equal %v), length %d, %v; want %d bytes", resp.Proto, k.path,
					len(b), bytes.Equal(b, want), resp.ContentLength, err, len(want))
			}
		}
		req, _ := http.NewRequest("HEAD", s.URL+"/held?sizes=70000", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.ContentLength != 70000 {
			t.Errorf("%s HEAD: %d, length %d", resp.Proto, resp.StatusCode, resp.ContentLength)
		}
		// A held chunk against a shorter or longer declared Content-Length: never complete.
		for _, q := range []string{"declared=10&actual=70000", "declared=70000&actual=20000"} {
			resp, err := c.Get(s.URL + "/cl?mode=list&" + q)
			if err != nil {
				continue // refused outright: fine
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				t.Errorf("%s %s: read %d bytes as a complete response (%d)", resp.Proto, q, len(b), resp.StatusCode)
			}
		}
	}
	// Concurrent requests, each its own content: the application's objects, freed when
	// its call returns, must stay alive and unchanged until written.
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				seed := g*1000 + i + 1
				sizes := "30000,70000,5000,131072"
				resp, err := plain.Client().Get(fmt.Sprintf("%s/held?seed=%d&sizes=%s", plain.URL, seed, sizes))
				if err != nil {
					t.Error(err)
					return
				}
				b, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || !bytes.Equal(b, heldBody(seed, sizes)) {
					mu.Lock()
					bad++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if bad > 0 {
		t.Errorf("%d of 320 concurrent responses were not their chunks", bad)
	}
	if n := count(); n != 0 {
		t.Errorf("%d body writes happened while Python's call was in progress; a known-length body has none", n)
	}

	// The controls: these do write mid-request, and the counter sees them.
	count = wsgi.CountDetachedWrites()
	get(t, plain.URL+"/stream?n=2") // streamed: each chunk at once
	streamed := count()
	count = wsgi.CountDetachedWrites()
	_, b := get(t, plain.URL+"/held?sizes=16000,1000,2000")
	if !bytes.Equal(b, heldBody(0, "16000,1000,2000")) {
		t.Errorf("/held?sizes=16000,1000,2000: %d bytes, not the chunks", len(b))
	}
	small := count()
	if streamed < 2 || small != 1 {
		t.Errorf("detached writes: %d for 2 streamed chunks (want 2 or more), %d for a small chunk past %d copied bytes (want 1)",
			streamed, small, 16<<10)
	}

	// The references are released: one object returned 3 times by 50 requests leaves its
	// reference count where it was.
	c := &http.Client{Transport: &http.Transport{}}
	refs := func() int {
		resp, err := c.Get(plain.URL + "/shared-refs")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		n, err := strconv.Atoi(string(b))
		if err != nil {
			t.Fatalf("/shared-refs: %q", b)
		}
		return n
	}
	before := refs()
	for range 50 {
		resp, err := c.Get(plain.URL + "/shared?n=3")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if after := refs(); after > before+1 {
		t.Errorf("SHARED's reference count went from %d to %d over 50 responses: references held for writing were not released", before, after)
	}
}
