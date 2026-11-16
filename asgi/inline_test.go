package asgi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
)

// inlineapp reports where each request ran: on the worker's loop thread or on a request
// goroutine's (inline), with which running loop and task, before and after it awaits.
const inlineapp = `
import asyncio, json, threading, time

loop_thread = None
inside = 0
CONST = b"constant body"

def exclusive():
    # The worker runs one thread's Python at a time, even across a call that releases
    # the GIL: a combining request and the loop thread never overlap.
    global inside
    inside += 1
    time.sleep(0.0002)
    n = inside
    inside -= 1
    if n != 1:
        raise RuntimeError("the worker ran two threads at once")

def where():
    loop = asyncio.get_running_loop()
    return {"loop_thread": threading.current_thread() is loop_thread,
            "task": asyncio.current_task() is not None,
            "loop": getattr(loop, "_pythago_marked", False)}

async def app(scope, receive, send):
    global loop_thread
    if scope["type"] == "lifespan":
        loop_thread = threading.current_thread()
        asyncio.get_running_loop()._pythago_marked = True
        while True:
            m = await receive()
            if m["type"] == "lifespan.startup":
                await send({"type": "lifespan.startup.complete"})
            else:
                await send({"type": "lifespan.shutdown.complete"})
                return
    path = scope["path"]
    if path in ("/const", "/refs"):
        import sys
        body = CONST if path == "/const" else str(sys.getrefcount(CONST)).encode()
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": body})
        return
    out = {"before": where()}
    if path == "/exclusive":
        exclusive()
        await asyncio.sleep(0)  # the rest runs on the loop thread
        exclusive()
        out["after"] = where()
    if path == "/sleep":
        await asyncio.sleep(0.001)
        out["after"] = where()
    elif path == "/task":
        async def child():
            return where()
        out["child"] = await asyncio.create_task(child())
        out["after"] = where()
    elif path == "/body":
        total, more = 0, True
        while more:
            m = await receive()
            total += len(m["body"])
            more = m["more_body"]
        out["total"] = total
        out["after"] = where()
    body = json.dumps(out).encode()
    await send({"type": "http.response.start", "status": 200,
                "headers": [(b"content-type", b"application/json")]})
    await send({"type": "http.response.body", "body": body})
`

type place struct {
	LoopThread bool `json:"loop_thread"`
	Task       bool `json:"task"`
	Loop       bool `json:"loop"`
}

type report struct {
	Before place  `json:"before"`
	After  *place `json:"after"`
	Child  *place `json:"child"`
	Total  int    `json:"total"`
}

func inlineServer(t *testing.T) *httptest.Server {
	t.Helper()
	return inlineappServer(t, asgi.Options{Workers: 1})
}

func inlineappServer(t *testing.T, opts asgi.Options) *httptest.Server {
	t.Helper()
	ts, _ := inlineappServers(t, opts)
	return ts
}

func inlineappServers(t *testing.T, opts asgi.Options) (*httptest.Server, *asgi.Server) {
	t.Helper()
	s, err := asgi.New("inlineapp:app", opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() {
		ts.Close()
		s.Close(context.Background())
	})
	return ts, s
}

func fetch(t testing.TB, client *http.Client, url, body string) (report, error) {
	var resp *http.Response
	var err error
	if body == "" {
		resp, err = client.Get(url)
	} else {
		resp, err = client.Post(url, "application/octet-stream", strings.NewReader(body))
	}
	if err != nil {
		return report{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return report{}, err
	}
	if resp.StatusCode != 200 {
		return report{}, fmt.Errorf("status %d: %s", resp.StatusCode, data)
	}
	var r report
	return r, json.Unmarshal(data, &r)
}

// TestInline checks that requests run inline, as eager tasks of the worker's loop (a
// running loop and a current task), and that a request awaiting anything the loop must
// deliver continues on the loop thread.
//
// Only a request that finds its worker free runs inline: one that arrives while the
// loop thread still holds it (after the startup, or finishing a request that continued
// there) runs on the loop thread, by design. So each request waits for the worker to be
// let go first; a request that then still ran on the loop thread is a regression.
func TestInline(t *testing.T) {
	ts, s := inlineappServers(t, asgi.Options{Workers: 1})
	c := ts.Client()
	inline := place{LoopThread: false, Task: true, Loop: true}
	onLoop := place{LoopThread: true, Task: true, Loop: true}
	fetchIdle := func(path, body string) (report, error) {
		if err := asgi.WaitIdle(s, 10*time.Second); err != nil {
			t.Fatal(path, err)
		}
		return fetch(t, c, ts.URL+path, body)
	}

	r, err := fetchIdle("/", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Before != inline {
		t.Errorf("/: ran at %+v, want inline %+v", r.Before, inline)
	}
	for _, path := range []string{"/sleep", "/task"} {
		r, err := fetchIdle(path, "")
		if err != nil {
			t.Fatal(path, err)
		}
		if r.Before != inline || r.After == nil || *r.After != onLoop {
			t.Errorf("%s: before %+v, after %+v; want inline, then the loop thread", path, r.Before, r.After)
		}
		if path == "/task" && (r.Child == nil || *r.Child != onLoop) {
			t.Errorf("/task: child task ran at %+v, want the loop thread", r.Child)
		}
	}
	// A body larger than one chunk: the first arrives with the request, the rest through
	// the loop thread.
	big := strings.Repeat("x", 300<<10)
	r, err = fetchIdle("/body", big)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != len(big) || r.Before != inline {
		t.Errorf("/body: total %d (want %d), ran at %+v", r.Total, len(big), r.Before)
	}
}

// TestLoopThread checks that Options.LoopThread opts out: every request runs on the
// worker's loop thread.
func TestLoopThread(t *testing.T) {
	ts := inlineappServer(t, asgi.Options{Workers: 1, LoopThread: true})
	onLoop := place{LoopThread: true, Task: true, Loop: true}
	for _, path := range []string{"/", "/sleep"} {
		r, err := fetch(t, ts.Client(), ts.URL+path, "")
		if err != nil {
			t.Fatal(path, err)
		}
		if r.Before != onLoop {
			t.Errorf("%s: ran at %+v, want the loop thread %+v", path, r.Before, onLoop)
		}
	}
}

// TestInlineConcurrent runs many concurrent requests, inline and falling back, so that
// requests combine while the loop thread runs others.
func TestInlineConcurrent(t *testing.T) {
	ts := inlineServer(t)
	tr := &http.Transport{MaxIdleConnsPerHost: 64}
	if runtime.GOOS == "windows" {
		// Windows refuses connects once the accept queue is full (WSAECONNREFUSED), and
		// Go's listen asks for SOMAXCONN, "a reasonable maximum" of a few hundred there:
		// 400 dials at once overflow it. Requests beyond these connections wait for one.
		tr.MaxConnsPerHost = 150
	}
	c := &http.Client{Transport: tr}
	paths := []string{"/", "/sleep", "/task", "/exclusive", "/"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for i := range 400 {
		wg.Go(func() {
			path := paths[i%len(paths)]
			r, err := fetch(t, c, ts.URL+path, "")
			if err == nil && path != "/" && (r.After == nil || !r.After.LoopThread) {
				err = fmt.Errorf("%s did not continue on the loop thread: %+v", path, r.After)
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	for i, err := range errs {
		if i == 5 {
			t.Errorf("... %d errors in all", len(errs))
			break
		}
		t.Error(err)
	}
}

// TestInlineReleases checks that the response bodies Go wrote from are released while
// requests run inline: with no request falling back, the loop thread may never poll,
// so the objects must not wait for it (they did: memory grew without bound).
func TestInlineReleases(t *testing.T) {
	ts := inlineServer(t)
	c := ts.Client()
	get := func(path string) string {
		resp, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	base := get("/refs")
	for range 300 {
		if b := get("/const"); b != "constant body" {
			t.Fatalf("got %q", b)
		}
	}
	var before, after int
	fmt.Sscan(base, &before)
	fmt.Sscan(get("/refs"), &after)
	if after > before+10 {
		t.Fatalf("references to a response body: %d before 300 requests, %d after", before, after)
	}
}
