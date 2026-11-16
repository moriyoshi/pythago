package asgi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moriyoshi/pythago/asgi"
	_ "github.com/moriyoshi/pythago/internal/testimage" // registers the native image and stdlib
	"github.com/moriyoshi/pythago/internal/testwatch"
	"github.com/moriyoshi/pythago/internal/testwheels"
	"github.com/moriyoshi/pythago/py"
)

// testapp is a framework-free ASGI application covering the protocol's corners.
const testapp = `
import asyncio, gc, json, threading, time, weakref

disconnects = []
slow_teardown = 0.0  # TestCloseJoinsWorkers: seconds each worker thread's teardown takes
_worker_local = threading.local()

async def wait_unreferenced():
    """Awaits a future only this coroutine holds: what resolves it holds it by a weakref,
    as asyncio.StreamReaderProtocol holds its StreamReader (and so the future a
    reader.readline() waits on). A collection meanwhile frees the request's task unless
    the server keeps it, and the task's coroutine sees GeneratorExit."""
    loop = asyncio.get_running_loop()
    fut = loop.create_future()
    ref = weakref.ref(fut)

    def resolve():
        f = ref()
        if f is not None and not f.done():
            f.set_result(None)

    loop.call_later(0.02, gc.collect)
    loop.call_later(0.06, resolve)
    await fut

class _SlowDel:
    def __init__(self, delay):
        self.delay = delay
    def __del__(self):
        time.sleep(self.delay)  # runs in the thread's PyThreadState_Clear

async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            msg = await receive()
            if msg["type"] == "lifespan.startup":
                scope["state"]["greeting"] = "hello from lifespan"
                if slow_teardown:
                    _worker_local.slow = _SlowDel(slow_teardown)  # this worker thread's own
                await send({"type": "lifespan.startup.complete"})
            else:
                await send({"type": "lifespan.shutdown.complete"})
                return
    if scope["type"] == "websocket":
        return await ws_routes(scope, receive, send)
    path = scope["path"]
    if path == "/echo":
        body = b""
        while True:
            msg = await receive()
            body += msg["body"]
            if not msg["more_body"]:
                break
        info = {"method": scope["method"], "path": scope["path"], "raw_path": scope["raw_path"].decode(),
                "query": scope["query_string"].decode(), "len": len(body),
                "headers": {k.decode(): v.decode() for k, v in scope["headers"]},
                "http_version": scope["http_version"], "scheme": scope["scheme"],
                "client": list(scope["client"]), "state": scope["state"].get("greeting")}
        await send({"type": "http.response.start", "status": 200,
                    "headers": [(b"content-type", b"application/json"), (b"x-body-sha256", __import__("hashlib").sha256(body).hexdigest().encode())]})
        await send({"type": "http.response.body", "body": json.dumps(info).encode()})
    elif path == "/stream":
        n = int(scope["query_string"] or b"4")
        await send({"type": "http.response.start", "status": 200, "headers": [(b"content-type", b"application/octet-stream")]})
        chunk = bytes(range(256)) * 256  # 64 KiB
        for i in range(n):
            await send({"type": "http.response.body", "body": chunk, "more_body": True})
        await send({"type": "http.response.body", "body": b""})
    elif path == "/error":
        raise ValueError("boom")
    elif path == "/error-after-start":
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": b"partial", "more_body": True})
        raise ValueError("boom after start")
    elif path == "/noresponse":
        return
    elif path == "/disconnect":
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": b"waiting", "more_body": True})
        while (await receive())["type"] != "http.disconnect":  # first: the (empty) request body
            pass
        disconnects.append("http.disconnect")
    elif path == "/disconnects":
        body = json.dumps(disconnects).encode()
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": body})
    elif path.startswith("/t/") or path.startswith("/h/"):
        await trailer_routes(scope, receive, send)
    elif path == "/p":  # pathsend: ?path=...[&cl=N][&trailers=1][&after=1]
        from urllib.parse import parse_qs
        q = {k: v[0] for k, v in parse_qs(scope["query_string"].decode()).items()}
        trailers = q.get("trailers") == "1"
        headers = [(b"content-type", b"application/octet-stream")]
        if "cl" in q:
            headers.append((b"content-length", q["cl"].encode()))
        await send({"type": "http.response.start", "status": 200, "headers": headers, "trailers": trailers})
        if q.get("after") == "1":
            await send({"type": "http.response.body", "body": b"x", "more_body": True})
        await send({"type": "http.response.pathsend", "path": q["path"]})
        if trailers:
            await send({"type": "http.response.trailers", "headers": [(b"x-sent", b"file")]})
    elif path == "/ws-errors":
        body = json.dumps(ws_errors).encode()
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": body})
    elif path == "/gc-wait":
        await wait_unreferenced()
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": b"survived"})
    elif path == "/empty":
        await send({"type": "http.response.start", "status": 204, "headers": []})
        await send({"type": "http.response.body"})
    else:
        await send({"type": "http.response.start", "status": 404, "headers": [(b"content-type", b"text/plain")]})
        await send({"type": "http.response.body", "body": ("not found: %s %s" % (path, scope["raw_path"].decode())).encode()})
`

// trailerapp holds the trailer and field-validation routes (/t/..., /h/...).
const trailerapp = `
import json

errors = []

def offered(scope):
    return "http.response.trailers" in scope.get("extensions", {})

async def start_body(send, trailers, headers=()):
    await send({"type": "http.response.start", "status": 200, "trailers": trailers,
                "headers": [(b"content-type", b"text/plain")] + list(headers)})
    await send({"type": "http.response.body", "body": b"part1 ", "more_body": True})
    await send({"type": "http.response.body", "body": b"part2"})

async def trailer_routes(scope, receive, send):
    path, q = scope["path"], scope["query_string"].decode()
    if path == "/t/ok":
        if not offered(scope):
            await send({"type": "http.response.start", "status": 200, "headers": [(b"x-offered", b"no")]})
            await send({"type": "http.response.body", "body": b"no trailers"})
            return
        await start_body(send, True, [(b"x-offered", b"yes")])
        await send({"type": "http.response.trailers", "headers": [(b"x-checksum", b"abc"), (b"grpc-status", b"0")],
                    "more_trailers": True})
        await send({"type": "http.response.trailers", "headers": [(b"x-second", b"2")]})
    elif path == "/t/single":  # one final body, then trailers: no flush happens on its own
        await send({"type": "http.response.start", "status": 200, "trailers": True, "headers": []})
        await send({"type": "http.response.body", "body": b"short"})
        await send({"type": "http.response.trailers", "headers": [(b"x-checksum", b"single")]})
    elif path == "/t/bad":
        # q: a trailer name (forbidden ones), or "value" (a CR/LF value), or "name"
        if q == "value":
            fields = [(b"x-ok", b"1\r\nset-cookie: evil=1")]
        elif q == "name":
            fields = [(b"x bad", b"1")]
        elif q == "many":
            fields = [(b"x-f%d" % i, b"v") for i in range(65)]
        else:
            fields = [(q.encode(), b"1")]
        await start_body(send, True)
        try:
            await send({"type": "http.response.trailers", "headers": fields})
        except Exception as exc:
            errors.append(("bad", q, str(exc)))
            raise
    elif path == "/t/notoffered":
        try:
            await send({"type": "http.response.start", "status": 200, "trailers": True, "headers": []})
        except RuntimeError as exc:
            errors.append(("notoffered", str(exc)))
            await send({"type": "http.response.start", "status": 200, "headers": []})
            await send({"type": "http.response.body", "body": b"fallback"})
    elif path == "/t/early":
        await send({"type": "http.response.start", "status": 200, "trailers": True, "headers": []})
        await send({"type": "http.response.body", "body": b"x", "more_body": True})
        try:
            await send({"type": "http.response.trailers", "headers": []})
        except RuntimeError as exc:
            errors.append(("early", str(exc)))
            raise
    elif path == "/t/missing":
        await start_body(send, True)  # and never sends the trailers
    elif path == "/t/clen":
        await send({"type": "http.response.start", "status": 200, "trailers": True,
                    "headers": [(b"content-length", b"11")]})
        await send({"type": "http.response.body", "body": b"part1 part2"})
    elif path == "/h/inject":
        await send({"type": "http.response.start", "status": 200,
                    "headers": [(b"x-a", b"1\x00set-cookie\x00evil=1")]})
        await send({"type": "http.response.body", "body": b"injected?"})
    elif path == "/h/crlf":
        await send({"type": "http.response.start", "status": 200,
                    "headers": [(b"x-a", b"1\r\nset-cookie: evil=1")]})
        await send({"type": "http.response.body", "body": b"split?"})
    elif path == "/h/badname":
        await send({"type": "http.response.start", "status": 200, "headers": [(b"x a", b"1")]})
        await send({"type": "http.response.body", "body": b"x"})
    elif path == "/h/status":
        await send({"type": "http.response.start", "status": int(q), "headers": []})
        await send({"type": "http.response.body", "body": b"x"})
    elif path == "/h/dupcl":
        await send({"type": "http.response.start", "status": 200,
                    "headers": [(b"content-length", b"1"), (b"content-length", b"5")]})
        await send({"type": "http.response.body", "body": b"x"})
    elif path == "/h/errors":
        body = json.dumps(errors).encode()
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": body})
`

var srv *asgi.Server

func config() py.Config {
	cfg := py.Config{Files: fstest.MapFS{
		"testapp.py":    {Data: []byte("from trailerapp import trailer_routes\nfrom wsapp import ws_routes, ws_errors\n" + testapp)},
		"trailerapp.py": {Data: []byte(trailerapp)},
		"wsapp.py":      {Data: []byte(wsapp)},
		"inlineapp.py":  {Data: []byte(inlineapp)},
		"ioapp.py":      {Data: []byte(ioapp)},
		"allocapp.py":   {Data: []byte(allocapp)},
		"heldbody.py":   {Data: []byte(heldbody)},
		"uploadecho.py": {Data: []byte(uploadecho)},
		"fusedecho.py":  {Data: []byte(fusedecho)},
		"sharedapp.py":  {Data: []byte(sharedapp)},
		"recycleapp.py": {Data: []byte(recycleapp)},
		"wheelsapp.py":  {Data: []byte(wheelsapp)},
	}}
	asgi.Configure(&cfg)
	// PYTHAGO_TEST_FUSION=1 runs the tests with the fusion on (Config.FusedBytes, 16 MiB).
	if os.Getenv("PYTHAGO_TEST_FUSION") == "1" {
		cfg.FusedBytes = 16 << 20
	}
	// PYTHAGO_TEST_WHEELS: real binary wheels on sys.path (wheels_test.go).
	site, err := testwheels.Site()
	if err != nil {
		fmt.Println("wheels:", err)
		os.Exit(1)
	}
	if site != "" {
		cfg.SearchPaths = append(cfg.SearchPaths, site)
	}
	return cfg
}

func TestMain(m *testing.M) {
	// WebSockets over HTTP/2 need extended CONNECT, which Go's HTTP/2 server reads from
	// GODEBUG at start-up: run the tests in a re-executed process that has it.
	if !strings.Contains(os.Getenv("GODEBUG"), "http2xconnect=1") && !py.IsPythonCommand() {
		exe, err := os.Executable()
		if err == nil {
			env := append(os.Environ(), "GODEBUG="+strings.TrimPrefix(os.Getenv("GODEBUG")+",http2xconnect=1", ","))
			err = reexec(exe, os.Args, env)
		}
		fmt.Println("re-exec with GODEBUG=http2xconnect=1:", err)
		os.Exit(1)
	}
	if py.IsPythonCommand() {
		os.Exit(py.Main(config(), os.Args))
	}
	disarm := testwatch.Arm("py.Init and asgi.New") // -test.timeout covers only m.Run
	if err := py.Init(config()); err != nil {
		fmt.Println("Init:", err)
		os.Exit(1)
	}
	var err error
	// PYTHAGO_ASGI_LOOP_THREAD=1 runs every test against a server without inline requests
	// (Options.LoopThread).
	if srv, err = asgi.New("testapp:app", asgi.Options{LoopThread: os.Getenv("PYTHAGO_ASGI_LOOP_THREAD") == "1"}); err != nil {
		fmt.Println("asgi.New:", err)
		os.Exit(1)
	}
	disarm()
	code := m.Run()
	testwatch.Arm("Close and py.Finalize") // armed through os.Exit
	if err := srv.Close(context.Background()); err != nil {
		fmt.Println("Close:", err)
		code = 1
	}
	if err := py.Finalize(); err != nil {
		fmt.Println("Finalize:", err)
		code = 1
	}
	testwheels.Remove()
	os.Exit(code)
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestEcho(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for _, size := range []int{0, 10, 64 << 10, 1 << 20} {
		payload := bytes.Repeat([]byte("abcdefgh"), size/8)
		req, _ := http.NewRequest("POST", ts.URL+"/echo?x=1&y=%20", bytes.NewReader(payload))
		req.Header.Set("X-Test", "yes")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var info struct {
			Method, Path, Query, State, Scheme string
			RawPath                            string `json:"raw_path"`
			HTTPVersion                        string `json:"http_version"`
			Len                                int
			Headers                            map[string]string
			Client                             []any
		}
		if err := json.Unmarshal(body, &info); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		sum := sha256.Sum256(payload)
		if info.Method != "POST" || info.Path != "/echo" || info.Query != "x=1&y=%20" || info.Len != len(payload) ||
			info.Headers["x-test"] != "yes" || info.Headers["host"] == "" || info.State != "hello from lifespan" ||
			info.HTTPVersion != "1.1" || info.Scheme != "http" || len(info.Client) != 2 ||
			resp.Header.Get("X-Body-Sha256") != fmt.Sprintf("%x", sum) {
			t.Errorf("size %d: %+v, sha %s", size, info, resp.Header.Get("X-Body-Sha256"))
		}
	}
}

func TestPathDecoding(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	// Unknown paths reach the application (404 from it, not from Go), with path decoded
	// and raw_path as sent.
	resp, body := get(t, ts, "/caf%C3%A9/a%2Fb")
	if resp.StatusCode != 404 || string(body) != "not found: /café/a/b /caf%C3%A9/a%2Fb" {
		t.Errorf("%d %q", resp.StatusCode, body)
	}
}

func TestStreamAndBackpressure(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, body := get(t, ts, "/stream?64") // 4 MiB in 64 KiB chunks, above the high-water mark
	chunk := make([]byte, 256)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	want := bytes.Repeat(chunk, 256*64)
	if resp.StatusCode != 200 || !bytes.Equal(body, want) {
		t.Errorf("status %d, %d bytes", resp.StatusCode, len(body))
	}
	// A slow reader: the application's sends wait for the client.
	resp2, err := ts.Client().Get(ts.URL + "/stream?32")
	if err != nil {
		t.Fatal(err)
	}
	var got int
	buf := make([]byte, 16<<10)
	for {
		n, err := resp2.Body.Read(buf)
		got += n
		if err != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	resp2.Body.Close()
	if got != 32<<16 {
		t.Errorf("slow reader got %d bytes", got)
	}
}

func TestErrors(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for _, p := range []string{"/error", "/noresponse"} {
		resp, body := get(t, ts, p)
		if resp.StatusCode != 500 || string(body) != "Internal Server Error" {
			t.Errorf("%s: %d %q", p, resp.StatusCode, body)
		}
	}
	// An error after the response started aborts the connection: the client sees a
	// truncated body.
	resp, err := ts.Client().Get(ts.URL + "/error-after-start")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil {
		t.Error("an aborted response read cleanly")
	}
	resp, body := get(t, ts, "/empty")
	if resp.StatusCode != 204 || len(body) != 0 {
		t.Errorf("/empty: %d %q", resp.StatusCode, body)
	}
}

// TestTaskKeptAlive: the server keeps each request's task while it runs, as uvicorn
// does. asyncio holds tasks weakly, and a request waiting on a future that only it
// holds (as one waiting in reader.readline() on asyncio.open_connection streams does)
// is otherwise freed by the next garbage collection: its coroutine gets GeneratorExit and
// the client a 500.
func TestTaskKeptAlive(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			resp, err := ts.Client().Get(ts.URL + "/gc-wait")
			if err != nil {
				t.Error(err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || string(body) != "survived" {
				t.Errorf("a request awaiting a future only it holds, across gc.collect(): %d %q",
					resp.StatusCode, body)
			}
		})
	}
	wg.Wait()
}

// A client that goes away is reported to receive() as http.disconnect.
func TestDisconnect(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/disconnect", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	io.ReadFull(resp.Body, buf)
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, body := get(t, ts, "/disconnects")
		if strings.Contains(string(body), "http.disconnect") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the application never saw http.disconnect")
}

func TestConcurrent(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := range 100 {
		wg.Go(func() {
			payload := bytes.Repeat([]byte{byte(i)}, 1000+i)
			resp, err := ts.Client().Post(ts.URL+"/echo", "application/octet-stream", bytes.NewReader(payload))
			if err != nil {
				errs <- err
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(body), fmt.Sprintf(`"len": %d`, 1000+i)) {
				errs <- fmt.Errorf("request %d: %s", i, body)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestCloseJoinsWorkers: when Close returns, the server's worker threads have exited,
// CPython's teardown of their thread states included. A worker still tearing down when
// Py_FinalizeEx runs can have its thread state cleared twice (3.14t crashed in
// _Py_brc_remove_thread at finalization on darwin).
func TestCloseJoinsWorkers(t *testing.T) {
	for _, loopThread := range []bool{false, true} {
		// The server's own Thread objects, as pythago_asgi.start recorded them: the entry
		// New adds to _threads (names prove nothing; any thread may take any name).
		if err := py.Do(func() error {
			return py.Exec("import pythago_asgi\n_close_before = set(pythago_asgi._threads)")
		}); err != nil {
			t.Fatal(err)
		}
		// Each worker's teardown takes 0.2 s, so a Close that does not wait for it is seen.
		setSlow := func(d string) {
			if err := py.Do(func() error { return py.Exec("import testapp\ntestapp.slow_teardown = " + d) }); err != nil {
				t.Fatal(err)
			}
		}
		setSlow("0.2")
		s, err := asgi.New("testapp:app", asgi.Options{Workers: 8, LoopThread: loopThread})
		setSlow("0.0")
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		if err := py.Do(func() error {
			if err := py.Exec(`import pythago_asgi
_close_new = [k for k in pythago_asgi._threads if k not in _close_before]
_close_join = list(pythago_asgi._threads[_close_new[0]]) if len(_close_new) == 1 else []`); err != nil {
				return err
			}
			r, err := py.Eval("len(_close_join)")
			if err != nil {
				return err
			}
			defer r.Close()
			n, err = r.Int64()
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if n != 8 {
			t.Fatalf("the new server's worker threads: %d, want 8", n)
		}
		ts := httptest.NewServer(s)
		for range 16 {
			if resp, err := http.Get(ts.URL + "/echo"); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}
		ts.Close()
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		var alive int64
		if err := py.Do(func() error {
			r, err := py.Eval("sum(t.is_alive() for t in _close_join)")
			if err != nil {
				return err
			}
			defer r.Close()
			alive, err = r.Int64()
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if alive != 0 {
			t.Errorf("LoopThread %v: %d worker threads still running when Close returned", loopThread, alive)
		}
	}
}
