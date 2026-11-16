package asgi_test

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
)

// recycleapp answers each request with its own id (the query string), in the header
// x-id and in every body chunk, along paths that exercise the recycled request state:
// /fast answers at once (inline: the first header pairs and body copy); /stream sends
// chunks with awaits between them (the loop thread); /slow awaits before answering, and
// /block holds its worker's thread, so that requests queue behind it, which the clients
// abandon at random moments (a Go function or a queued batch may still hold a request
// whose handler has returned).
const recycleapp = `
import asyncio, time

async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            m = await receive()
            if m["type"] == "lifespan.startup":
                await send({"type": "lifespan.startup.complete"})
            else:
                await send({"type": "lifespan.shutdown.complete"})
                return
    rid = scope["query_string"]
    path = scope["path"]
    headers = [(b"x-id", rid), (b"content-type", b"text/plain")]
    if path == "/block":
        time.sleep(0.0005)
    elif path == "/slow":
        await asyncio.sleep(0.001)
    if path == "/stream":
        await send({"type": "http.response.start", "status": 200, "headers": headers})
        for i in range(3):
            await send({"type": "http.response.body", "body": rid + b";", "more_body": True})
            await asyncio.sleep(0)
        await send({"type": "http.response.body", "body": rid + b"."})
        return
    await send({"type": "http.response.start", "status": 200, "headers": headers})
    await send({"type": "http.response.body", "body": rid + b"."})
`

// lostAfter is when TestRecycleNoCrossTalk takes a response it waits for as lost.
const lostAfter = 5 * time.Second

// TestRecycleNoCrossTalk runs many concurrent requests, some abandoned by their clients
// at random moments, and checks that every answered request got its own response: a
// request recycled while something still used it would pass that one's actions to the
// next request (another id in the header or the body), or lose them (no response).
func TestRecycleNoCrossTalk(t *testing.T) {
	s, err := asgi.New("recycleapp:app", asgi.Options{Workers: 2,
		LoopThread: os.Getenv("PYTHAGO_ASGI_LOOP_THREAD") == "1"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	defer s.Close(context.Background())
	defer ts.Close()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	defer client.CloseIdleConnections()

	paths := []string{"/fast", "/fast", "/stream", "/slow", "/block"}
	var seq atomic.Int64
	var answered, abandoned atomic.Int64
	var mu sync.Mutex
	var errs []string
	var failed atomic.Bool // stop at the first errors
	fail := func(format string, a ...any) {
		mu.Lock()
		errs = append(errs, fmt.Sprintf(format, a...))
		mu.Unlock()
		failed.Store(true)
	}
	var wg sync.WaitGroup
	for g := range 32 {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(g), 1))
			for range 60 {
				if failed.Load() {
					return
				}
				id := fmt.Sprintf("r%d", seq.Add(1))
				path := paths[rng.IntN(len(paths))]
				abandon := rng.IntN(3) == 0 // at some point
				timeout := lostAfter
				if abandon {
					timeout = time.Duration(rng.IntN(3000)) * time.Microsecond
				}
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+path+"?"+id, nil)
				resp, err := client.Do(req)
				var body []byte
				if err == nil {
					body, err = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
				expired := ctx.Err() != nil
				cancel()
				switch {
				case err != nil && abandon:
					abandoned.Add(1)
					continue
				case err != nil && expired:
					fail("%s?%s: no response within %v (lost)", path, id, lostAfter)
					continue
				case err != nil:
					fail("%s?%s: %v", path, id, err)
					continue
				}
				want := id + "."
				if path == "/stream" {
					want = strings.Repeat(id+";", 3) + id + "."
				}
				if resp.StatusCode != 200 || resp.Header.Get("X-Id") != id || string(body) != want {
					fail("%s?%s: status %d, x-id %q, body %q; want 200, %q, %q",
						path, id, resp.StatusCode, resp.Header.Get("X-Id"), body, id, want)
				}
				answered.Add(1)
			}
		})
	}
	wg.Wait()
	t.Logf("%d answered, %d abandoned", answered.Load(), abandoned.Load())
	for i, e := range errs {
		if i == 10 {
			t.Errorf("... %d errors in all", len(errs))
			break
		}
		t.Error(e)
	}
	if answered.Load() == 0 {
		t.Fatal("no request was answered")
	}
}

// TestRecycleHeldRequest holds a request past its handler's return, deterministically,
// where TestRecycleNoCrossTalk relies on timing (a window of microseconds with
// Options.LoopThread). A test hook (asgi.HoldCall) stops a _pythago_asgi call after it has
// taken its reference to request A; A's client goes away, so A's handler returns (finish)
// while the call still holds A; then request B arrives, and only then does the call go on.
// GOMAXPROCS is 1 meanwhile, so that B's handler takes from the pool what A's finish put
// there, if anything: a request recycled at finish regardless of that reference is B's by
// then. The test checks that the held call's request is still A's when it goes on (its
// handle), and B's answer.
//
// B's answer alone does not tell: the held call pushes A's response to B's request, but its
// own last reference then recycles that again, which drops the queued actions and the
// kick before B's handler looks (with Options.LoopThread, B's own response then goes
// through the recycled request and arrives intact). A's id is the longer one: should A's
// actions reach B's handler, they refer to the request's kept header pairs and body copy,
// which B's own response overwrites in place, and with ids of one length B's answer would
// read as B's own.
//
// Subtests:
//
//   - call: the held call is A's own respond, on the loop thread (/slow awaits first), in
//     both modes;
//   - batch (inline only): A is queued behind a held call of request C, and is abandoned
//     while it waits in the batch; a batch that did not hold A would run B twice.
func TestRecycleHeldRequest(t *testing.T) {
	loopThread := os.Getenv("PYTHAGO_ASGI_LOOP_THREAD") == "1"
	t.Run("call", func(t *testing.T) {
		h := newHeldServer(t, loopThread)
		a := h.get("/slow?held-A")
		h.waitHeld()
		h.cancel(a)
		h.waitFor("A to finish", func() bool { return asgi.Requests(h.s) == 0 })
		b := h.get("/fast?B")
		h.waitFor("B to arrive", func() bool { return asgi.Requests(h.s) == 1 })
		h.goOn()
		h.check(b, "B", "B.")
	})
	t.Run("batch", func(t *testing.T) {
		if loopThread {
			t.Skip("inline only: Options.LoopThread posts copies, it has no batch")
		}
		h := newHeldServer(t, loopThread)
		c := h.get("/slow?C")
		h.waitHeld()
		a := h.get("/fast?held-A")
		h.waitFor("A to queue", func() bool { return asgi.Queued(h.s) == 1 })
		h.cancel(a)
		h.waitFor("A to finish", func() bool { return asgi.Requests(h.s) == 1 })
		b := h.get("/stream?B")
		h.waitFor("B to queue", func() bool { return asgi.Queued(h.s) == 2 })
		h.goOn()
		h.check(c, "C", "C.")
		h.check(b, "B", "B;B;B;B.")
	})
}

type heldServer struct {
	t       *testing.T
	s       *asgi.Server
	url     string
	client  *http.Client
	held    <-chan struct{}
	release func() (before, after int64, ok bool)
}

type heldResult struct {
	cancel context.CancelFunc
	done   chan struct{}
	resp   *http.Response
	body   []byte
	err    error
}

// newHeldServer starts recycleapp on one worker, with the first _pythago_asgi call that
// finds its request held until h.goOn.
func newHeldServer(t *testing.T, loopThread bool) *heldServer {
	prev := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
	s, err := asgi.New("recycleapp:app", asgi.Options{Workers: 1, LoopThread: loopThread})
	if err != nil {
		t.Fatal(err)
	}
	h := &heldServer{t: t, s: s}
	h.held, h.release = asgi.HoldCall(s)
	ts := httptest.NewServer(s)
	h.url = ts.URL
	h.client = &http.Client{Transport: &http.Transport{}}
	t.Cleanup(func() { s.Close(context.Background()) })
	t.Cleanup(ts.Close)
	t.Cleanup(h.client.CloseIdleConnections)
	t.Cleanup(func() { h.release() }) // first: nothing stays held when a check fails
	return h
}

func (h *heldServer) get(path string) *heldResult {
	ctx, cancel := context.WithCancel(context.Background())
	r := &heldResult{cancel: cancel, done: make(chan struct{})}
	h.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", h.url+path, nil)
	go func() {
		defer close(r.done)
		r.resp, r.err = h.client.Do(req)
		if r.err == nil {
			r.body, r.err = io.ReadAll(r.resp.Body)
			r.resp.Body.Close()
		}
	}()
	return r
}

func (h *heldServer) waitHeld() {
	h.t.Helper()
	select {
	case <-h.held:
	case <-time.After(lostAfter):
		h.t.Fatalf("no _pythago_asgi call within %v", lostAfter)
	}
}

// goOn lets the held call go on, and checks that its request was not recycled meanwhile.
func (h *heldServer) goOn() {
	h.t.Helper()
	before, after, ok := h.release()
	switch {
	case !ok:
		h.t.Error("no _pythago_asgi call was held")
	case after != before:
		h.t.Errorf("the request a _pythago_asgi call held (handle %d) was recycled under it: "+
			"it went on with request %d", before, after)
	}
}

// cancel abandons r and waits for its client to give up.
func (h *heldServer) cancel(r *heldResult) {
	h.t.Helper()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(lostAfter):
		h.t.Fatalf("an abandoned request's client still waits after %v", lostAfter)
	}
	if r.err == nil {
		h.t.Fatalf("an abandoned request was answered (%d %q) before it was abandoned", r.resp.StatusCode, r.body)
	}
}

func (h *heldServer) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(lostAfter)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("waited %v for %s", lostAfter, what)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

func (h *heldServer) check(r *heldResult, id, body string) {
	h.t.Helper()
	select {
	case <-r.done:
	case <-time.After(lostAfter):
		h.t.Fatalf("request %s: no response within %v (lost)", id, lostAfter)
	}
	if r.err != nil {
		h.t.Fatalf("request %s: %v", id, r.err)
	}
	if r.resp.StatusCode != 200 || r.resp.Header.Get("X-Id") != id || string(r.body) != body {
		h.t.Errorf("request %s: status %d, x-id %q, body %q; want 200, %q, %q",
			id, r.resp.StatusCode, r.resp.Header.Get("X-Id"), r.body, id, body)
	}
}
