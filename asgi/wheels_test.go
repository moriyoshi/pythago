package asgi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
	"github.com/moriyoshi/pythago/internal/testwheels"
	"github.com/moriyoshi/pythago/py"
)

// wheelsapp awaits native code from real binary wheels (internal/testwheels) inside ASGI
// requests: pyreqwest, whose futures tokio's threads complete through
// loop.call_soon_threadsafe, calls the Go server named by the x-backend header (a
// testwheels.Server). Each worker's lifespan makes the worker's clients. Routes:
//
//	/info          the worker's loop type and whether the GIL is enabled
//	/fan?id&n      n backend requests at once; their bodies joined by ","
//	/big?n         a backend body of n bytes; its sha256 and length
//	/timeout?tag   a client with a 200 ms timeout on a held backend request: "timeout"
//	/cancel?tag    a held backend request cancelled once the backend has it, then
//	               released, and another request: its body
//	/zmq?n         n zmq.asyncio PUSH/PULL messages over tcp loopback on the worker's
//	               loop (GoEventLoop's add_reader): "received n"
//
// A failure answers 500 with the traceback.
const wheelsapp = `
import asyncio, hashlib, json, sys, sysconfig, threading, traceback
from datetime import timedelta
from urllib.parse import parse_qsl
from pyreqwest.client import ClientBuilder
from pyreqwest import exceptions as rqx
import zmq, zmq.asyncio

T = 60  # seconds: the bound on every await

async def fetch(c, url):
    r = await asyncio.wait_for(c.get(url).build().send(), T)
    if r.status != 200:
        raise RuntimeError("%s: %d" % (url, r.status))
    return bytes(await asyncio.wait_for(r.bytes(), T))

async def wait_stats(c, backend, tag, want, what):
    loop = asyncio.get_running_loop()
    deadline = loop.time() + T
    while True:
        st = json.loads(await fetch(c, "%s/stats?tag=%s" % (backend, tag)))
        if want(st):
            return
        if loop.time() > deadline:
            raise AssertionError("%s: %s after %ds" % (what, st, T))
        await asyncio.sleep(0.01)

async def route(scope, path, q, backend):
    c = scope["state"]["client"]
    if path == "/info":
        gil = sys._is_gil_enabled() if hasattr(sys, "_is_gil_enabled") else True
        return json.dumps({"loop": type(asyncio.get_running_loop()).__name__, "gil": gil,
                           "ft": bool(sysconfig.get_config_var("Py_GIL_DISABLED"))}).encode()
    if path == "/fan":
        parts = await asyncio.wait_for(asyncio.gather(
            *(fetch(c, "%s/hello?i=%s-%d" % (backend, q["id"], j)) for j in range(int(q["n"])))), T)
        return b",".join(parts)
    if path == "/big":
        data = await fetch(c, "%s/bytes?n=%s" % (backend, q["n"]))
        return b"%s %d" % (hashlib.sha256(data).hexdigest().encode(), len(data))
    if path == "/timeout":
        try:
            await asyncio.wait_for(scope["state"]["timeout_client"].get(
                "%s/slow?tag=%s" % (backend, q["tag"])).build().send(), T)
        except rqx.RequestTimeoutError:
            await fetch(c, "%s/release?tag=%s" % (backend, q["tag"]))
            return b"timeout"
        raise AssertionError("no timeout")
    if path == "/cancel":
        tag = q["tag"]
        task = asyncio.ensure_future(c.get("%s/slow?tag=%s" % (backend, tag)).build().send())
        await wait_stats(c, backend, tag, lambda s: s["inflight"] == 1, "the slow request never reached the backend")
        task.cancel()
        try:
            await asyncio.wait_for(task, T)
        except asyncio.CancelledError:
            pass
        else:
            raise AssertionError("the cancelled request completed")
        # pyreqwest leaves the request running in tokio (as under stock CPython): the
        # backend answers it on release, and that late completion must not disturb the loop.
        await fetch(c, "%s/release?tag=%s" % (backend, tag))
        await wait_stats(c, backend, tag, lambda s: s["inflight"] == 0, "the released request is still held")
        return await fetch(c, "%s/hello?i=after-%s" % (backend, tag))
    if path == "/zmq":
        n = int(q["n"])
        ctx = zmq.asyncio.Context()
        try:
            pull, push = ctx.socket(zmq.PULL), ctx.socket(zmq.PUSH)
            push.sndhwm = 10  # backpressure: sends wait for the receiver
            push.connect("tcp://127.0.0.1:%d" % pull.bind_to_random_port("tcp://127.0.0.1"))
            async def sender():
                for i in range(n):
                    await push.send(b"%d" % i)
            snd = asyncio.ensure_future(sender())
            for i in range(n):
                m = await asyncio.wait_for(pull.recv(), T)
                if m != b"%d" % i:
                    raise AssertionError("message %d: %r" % (i, m))
            await asyncio.wait_for(snd, T)
            push.close(linger=0)
            pull.close(linger=0)
        finally:
            ctx.destroy(linger=0)
        return b"received %d" % n
    raise LookupError(path)

async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            m = await receive()
            if m["type"] == "lifespan.startup":
                scope["state"]["client"] = ClientBuilder().build()
                scope["state"]["timeout_client"] = ClientBuilder().timeout(timedelta(milliseconds=200)).build()
                await send({"type": "lifespan.startup.complete"})
            else:
                await scope["state"]["client"].close()
                await scope["state"]["timeout_client"].close()
                await send({"type": "lifespan.shutdown.complete"})
                return
    backend = dict(scope["headers"])[b"x-backend"].decode()
    q = dict(parse_qsl(scope["query_string"].decode()))
    status = 200
    try:
        body = await route(scope, scope["path"], q, backend)
    except Exception:
        status, body = 500, traceback.format_exc().encode()
    await send({"type": "http.response.start", "status": status, "headers": [(b"content-type", b"text/plain")]})
    await send({"type": "http.response.body", "body": body})
`

// TestWheelsUnderLoad serves wheelsapp under concurrent HTTP load (inline, or with
// PYTHAGO_ASGI_LOOP_THREAD=1 on the loop thread): a mix of fan-outs, large bodies,
// client timeouts and cancellations, every one awaiting pyreqwest, and zmq.asyncio
// exchanges on the workers' GoEventLoop.
func TestWheelsUnderLoad(t *testing.T) {
	testwheels.Skip(t)
	// The workers import wheelsapp, and they exit at Close. mimalloc (in pyreqwest) takes
	// the thread it is first used on as its main thread, by thread pointer, and a thread
	// started later on that thread's cached stack then shares its heap: stock CPython
	// crashes the same way when pyreqwest is first imported on a thread that exits. Import
	// it first here, on an OS thread that ran Do, which outlives any Python thread.
	if err := py.Do(func() error { return py.Exec("import pyreqwest.client, zmq") }); err != nil {
		t.Fatal(err)
	}
	backend := testwheels.NewServer(t)
	s, err := asgi.New("wheelsapp:app", asgi.Options{Workers: 4,
		LoopThread: os.Getenv("PYTHAGO_ASGI_LOOP_THREAD") == "1"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	defer s.Close(context.Background())
	defer ts.Close()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}, Timeout: 3 * time.Minute}
	defer client.CloseIdleConnections()
	get := func(path string) (int, string, error) {
		req, err := http.NewRequest("GET", ts.URL+path, nil)
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("x-backend", backend.URL)
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), err
	}

	code, body, err := get("/info")
	if err != nil || code != 200 {
		t.Fatalf("/info: %d %v\n%s", code, err, body)
	}
	var info struct {
		Loop    string
		GIL, FT bool
	}
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatal(err, body)
	}
	t.Logf("/info: %s", body)
	if info.Loop != "GoEventLoop" || info.FT && info.GIL {
		t.Errorf("/info: %+v", info)
	}

	zmqWhy := testwheels.ZmqUnsupported(runtime.GOOS, runtime.GOARCH)
	if zmqWhy != "" {
		t.Logf("/zmq skipped: %s", zmqWhy)
	}
	const total, parallel = 400, 100
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	t0 := time.Now()
	for i := range total {
		var path, want string
		switch {
		case i%40 == 0:
			tag := fmt.Sprintf("asgi-cancel-%d", i)
			path, want = "/cancel?tag="+tag, "hello after-"+tag
		case i%40 == 1:
			path, want = fmt.Sprintf("/timeout?tag=asgi-timeout-%d", i), "timeout"
		case i%20 == 3 && zmqWhy == "":
			path, want = "/zmq?n=200", "received 200"
		case i%10 == 2:
			n := 1<<20 + i
			pat := make([]byte, n)
			for j := range pat {
				pat[j] = byte(j)
			}
			path, want = fmt.Sprintf("/big?n=%d", n), fmt.Sprintf("%x %d", sha256.Sum256(pat), n)
		default:
			parts := make([]string, 5)
			for j := range parts {
				parts[j] = fmt.Sprintf("hello %d-%d", i, j)
			}
			path, want = fmt.Sprintf("/fan?id=%d&n=5", i), strings.Join(parts, ",")
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			code, body, err := get(path)
			if err != nil || code != 200 || body != want {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %d %v\n%s", path, code, err, body))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	t.Logf("%d requests, %d at once: %v", total, parallel, time.Since(t0))
	for i, f := range failures {
		if i == 10 {
			t.Errorf("... and %d more", len(failures)-10)
			break
		}
		t.Error(f)
	}
}
