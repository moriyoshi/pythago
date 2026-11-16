package asgi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
)

// ioapp does real I/O in ASGI handlers: an HTTP proxy over asyncio streams (the worker's
// Go-backed loop) and over urllib in executor threads, sqlite3 on the loop and in
// threads, files, name resolution and subprocesses.
const ioapp = `
import asyncio, gc, hashlib, json, logging, os, socket, sqlite3, subprocess, sys, threading, time
import urllib.error, urllib.request
from urllib.parse import parse_qs

log_records = []  # what the server logs (pythago.asgi)

class _Capture(logging.Handler):
    def emit(self, record):
        log_records.append(logging.Formatter().format(record))  # with the traceback

logging.getLogger("pythago.asgi").addHandler(_Capture())

HOP = {b"connection", b"keep-alive", b"transfer-encoding", b"te", b"trailer", b"upgrade",
       b"proxy-connection"}
_lock = threading.Lock()
stats = {"active": 0, "opened": 0, "closed": 0}

def count(key, d):
    with _lock:
        stats[key] += d

def query(scope):
    return {k: v[0] for k, v in parse_qs(scope["query_string"].decode()).items()}

async def read_body(receive):
    parts = []
    while True:
        m = await receive()
        if m["type"] == "http.disconnect":
            raise ConnectionError("the client went away")
        parts.append(m.get("body", b""))
        if not m.get("more_body"):
            return b"".join(parts)

async def respond(send, status, body, headers=()):
    await send({"type": "http.response.start", "status": status, "headers": list(headers)})
    await send({"type": "http.response.body", "body": body})

async def respond_json(send, obj, status=200):
    await respond(send, status, json.dumps(obj).encode(), [(b"content-type", b"application/json")])

# An HTTP/1.1 client on asyncio streams: asyncio.open_connection on the worker's loop.
async def upstream_open(up, method, path, body=b"", extra=()):
    host, port = up.rsplit(":", 1)
    reader, writer = await asyncio.open_connection(host, int(port))
    try:
        head = ["%s %s HTTP/1.1" % (method, path), "Host: " + up, "Connection: close",
                "Content-Length: %d" % len(body)] + ["%s: %s" % kv for kv in extra]
        writer.write(("\r\n".join(head) + "\r\n\r\n").encode("latin-1") + body)
        await writer.drain()
        status = int((await reader.readline()).split()[1])
        headers = []
        while True:
            line = await reader.readline()
            if line in (b"\r\n", b"\n", b""):
                break
            k, _, v = line.partition(b":")
            headers.append((k.strip().lower(), v.strip()))
    except BaseException:
        writer.close()
        raise
    return status, headers, reader, writer

async def upstream_body(reader, headers):
    h = dict(headers)
    if h.get(b"transfer-encoding", b"").lower() == b"chunked":
        while True:
            size = int((await reader.readline()).split(b";")[0], 16)
            if size == 0:
                while (await reader.readline()) not in (b"\r\n", b""):
                    pass
                return
            yield await reader.readexactly(size)
            await reader.readexactly(2)
    elif b"content-length" in h:
        left = int(h[b"content-length"])
        while left:
            b = await reader.read(min(left, 65536))
            if not b:
                raise ConnectionError("the upstream closed early")
            left -= len(b)
            yield b
    else:
        while b := await reader.read(65536):
            yield b

async def proxy(scope, receive, send, q):
    """Relays the upstream response as it arrives, and stops when the client goes away
    (as Starlette's StreamingResponse does: send() after a disconnect does nothing)."""
    body = await read_body(receive) if scope["method"] == "POST" else b""
    if "gc" in q:  # a collection while the request waits for the upstream's response
        asyncio.get_running_loop().call_later(float(q["gc"]), gc.collect)
    status, headers, reader, writer = await upstream_open(q["up"], scope["method"], q["p"], body,
                                                          [("X-Fwd", "asgi-streams")])
    count("active", 1)
    count("opened", 1)

    async def pump():
        await send({"type": "http.response.start", "status": status,
                    "headers": [(k, v) for k, v in headers if k not in HOP]})
        async for chunk in upstream_body(reader, headers):
            await send({"type": "http.response.body", "body": chunk, "more_body": True})
        await send({"type": "http.response.body", "body": b""})

    async def watch():
        while (await receive())["type"] != "http.disconnect":
            pass

    try:
        pumping, watching = asyncio.ensure_future(pump()), asyncio.ensure_future(watch())
        done, pending = await asyncio.wait({pumping, watching}, return_when=asyncio.FIRST_COMPLETED)
        for t in pending:
            t.cancel()
        await asyncio.gather(*pending, return_exceptions=True)
        if pumping in done:
            pumping.result()
    finally:
        writer.close()
        try:
            await writer.wait_closed()
        except OSError:
            pass
        count("active", -1)
        count("closed", 1)

def urlopen(url, data=None, fwd="thread"):
    req = urllib.request.Request(url, data=data, headers={"X-Fwd": fwd})
    try:
        return urllib.request.urlopen(req, timeout=30)
    except urllib.error.HTTPError as e:
        return e

def fetch(url, data=None):
    with urlopen(url, data) as r:
        return r.getcode(), r.headers.items(), r.read()

def relay_headers(items):
    return [(k.lower().encode("latin-1"), v.encode("latin-1")) for k, v in items
            if k.lower().encode("latin-1") not in HOP]

async def tproxy(scope, receive, send, q):
    """urllib off the loop: the whole fetch in asyncio.to_thread, or each read in
    run_in_executor (streamed)."""
    url = "http://%s%s" % (q["up"], q["p"])
    data = await read_body(receive) if scope["method"] == "POST" else None
    if q["mode"] == "thread":
        status, items, body = await asyncio.to_thread(fetch, url, data)
        await respond(send, status, body, relay_headers(items))
        return
    loop = asyncio.get_running_loop()
    r = await loop.run_in_executor(None, urlopen, url, data, "executor")
    try:
        await send({"type": "http.response.start", "status": r.getcode(),
                    "headers": relay_headers(r.headers.items())})
        while chunk := await loop.run_in_executor(None, r.read, 65536):
            await send({"type": "http.response.body", "body": chunk, "more_body": True})
        await send({"type": "http.response.body", "body": b""})
    finally:
        await loop.run_in_executor(None, r.close)

# sqlite3: one connection per call, opened where it is used.
def db_connect(path, timeout=20.0, **kw):
    return sqlite3.connect(path, timeout=timeout, isolation_level=None, **kw)

def db_init(path):
    c = db_connect(path)
    c.execute("PRAGMA journal_mode=WAL")
    c.execute("CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, writer INTEGER, seq INTEGER, payload BLOB)")
    c.execute("CREATE TABLE IF NOT EXISTS counter (k TEXT PRIMARY KEY, v INTEGER)")
    c.execute("INSERT OR IGNORE INTO counter VALUES ('rows', 0)")
    c.close()

def db_write(path, writer, n, crash=False):
    """One transaction: n rows and a read-modify-write of the counter."""
    c = db_connect(path)
    try:
        c.execute("BEGIN IMMEDIATE")
        (v,) = c.execute("SELECT v FROM counter WHERE k = 'rows'").fetchone()
        time.sleep(0.001)  # widen the window a lost update would need
        c.executemany("INSERT INTO t (writer, seq, payload) VALUES (?, ?, ?)",
                      [(writer, i, os.urandom(64)) for i in range(n)])
        if crash:
            raise RuntimeError("crash inside a transaction")
        c.execute("UPDATE counter SET v = ? WHERE k = 'rows'", (v + n,))
        c.execute("COMMIT")
    except BaseException:
        if c.in_transaction:
            c.execute("ROLLBACK")
        raise
    finally:
        c.close()

def db_read(path):
    c = db_connect(path)
    try:
        c.execute("BEGIN")
        (n,) = c.execute("SELECT count(*) FROM t").fetchone()
        (v,) = c.execute("SELECT v FROM counter WHERE k = 'rows'").fetchone()
        c.execute("COMMIT")
        return {"count": n, "counter": v}
    finally:
        c.close()

async def db(scope, receive, send, q):
    op, path, thread = q["op"], q["db"], q.get("mode") == "thread"
    run = (lambda f, *a: asyncio.to_thread(f, *a)) if thread else None
    if op == "init":
        db_init(path)
        await respond_json(send, {})
    elif op in ("write", "crash"):
        args = (path, int(q["w"]), int(q["n"]), op == "crash")
        if thread:
            await asyncio.to_thread(db_write, *args)
        else:
            db_write(*args)  # blocking, on whichever thread runs the request
        await respond_json(send, {})
    elif op == "read":
        await respond_json(send, await asyncio.to_thread(db_read, path) if thread else db_read(path))
    elif op == "affinity":
        await affinity(scope, receive, send, q)

async def affinity(scope, receive, send, q):
    """A connection opened before an await and used after it. Inline, the request
    continues on the loop thread after an await the loop must complete (documented:
    sqlite3's same-thread check sees a per-batch thread)."""
    c = sqlite3.connect(q["db"], check_same_thread=q["cst"] == "1")
    threads = [threading.get_ident()]
    errors = []
    c.execute("SELECT count(*) FROM t").fetchone()
    if q["await"] == "sleep":
        await asyncio.sleep(0.005)
        threads.append(threading.get_ident())
        try:
            c.execute("SELECT count(*) FROM t").fetchone()
        except sqlite3.ProgrammingError as e:
            errors.append(str(e))
        body = b""
    else:  # stream 2 MiB, past the high-water mark, touching the connection per chunk
        await send({"type": "http.response.start", "status": 200, "headers": []})
        for i in range(32):
            threads.append(threading.get_ident())
            try:
                c.execute("SELECT ?", (i,)).fetchone()
            except sqlite3.ProgrammingError as e:
                errors.append(str(e))
            await send({"type": "http.response.body", "body": b"x" * 65535 + b"\n", "more_body": True})
    try:
        c.close()
    except sqlite3.ProgrammingError:
        del c  # closed when collected: now, so that the file is not left open (Windows
        gc.collect()  # cannot remove an open file)
    summary = json.dumps({"threads": len(set(threads)), "errors": errors}).encode()
    if q["await"] == "sleep":
        await respond_json(send, json.loads(summary))
    else:
        await send({"type": "http.response.body", "body": summary})

def read_file(path, chunk):
    with open(path, "rb") as f:
        return f.read(chunk)

async def files(scope, receive, send, q):
    if scope["path"] == "/file":
        chunk = int(q.get("chunk", 1 << 20))
        await send({"type": "http.response.start", "status": 200, "headers": []})
        with open(q["path"], "rb") as f:
            while True:
                b = await asyncio.to_thread(f.read, chunk) if q["mode"] == "thread" else f.read(chunk)
                if not b:
                    break
                await send({"type": "http.response.body", "body": b, "more_body": True})
        await send({"type": "http.response.body", "body": b""})
    else:  # /upload: streamed into the file as it arrives
        h = hashlib.sha256()
        n = 0
        with open(q["path"], "wb") as f:
            while True:
                m = await receive()
                b = m.get("body", b"")
                h.update(b)
                n += len(b)
                if q["mode"] == "thread":
                    await asyncio.to_thread(f.write, b)
                else:
                    f.write(b)
                if not m.get("more_body"):
                    break
        await respond_json(send, {"sha256": h.hexdigest(), "len": n})

async def dns(scope, receive, send, q):
    loop = asyncio.get_running_loop()
    port = int(q["up"].rsplit(":", 1)[1])
    go = await loop.getaddrinfo("localhost", port, type=socket.SOCK_STREAM)
    libc = await loop.run_in_executor(None, lambda: socket.getaddrinfo("localhost", port, type=socket.SOCK_STREAM))
    status, headers, reader, writer = await upstream_open("localhost:%d" % port, "GET", "/data?n=100000&seed=9")
    try:
        body = b"".join([b async for b in upstream_body(reader, headers)])
    finally:
        writer.close()
    await respond_json(send, {"go": sorted({a[4][0] for a in go}), "libc": sorted({a[4][0] for a in libc}),
                              "status": status, "sha256": hashlib.sha256(body).hexdigest()})

SCRIPT = "cat; echo done-$PYTHAGO_X"
COMMAND = ["/bin/sh", "-c", SCRIPT]
if sys.platform == "win32":  # no sh: the same in Python (this test binary)
    COMMAND = [sys.executable, "-c", "import os, shutil, sys; "
               "shutil.copyfileobj(sys.stdin.buffer, sys.stdout.buffer); "
               "sys.stdout.buffer.write(('done-' + os.environ['PYTHAGO_X'] + chr(10)).encode())"]

async def proc(scope, receive, send, q):
    data = await read_body(receive)
    env = dict(os.environ, PYTHAGO_X=q.get("x", ""))
    if q["mode"] == "thread":
        r = await asyncio.to_thread(subprocess.run, COMMAND, input=data,
                                    capture_output=True, env=env, timeout=20)
        out, rc = r.stdout, r.returncode
    else:
        p = await asyncio.create_subprocess_exec(*COMMAND, env=env,
            stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE)
        out, _ = await p.communicate(data)
        rc = p.returncode
    await respond(send, 200, out, [(b"x-rc", str(rc).encode())])

async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            m = await receive()
            await send({"type": m["type"] + ".complete"})
            if m["type"] == "lifespan.shutdown":
                return
    q, path = query(scope), scope["path"]
    try:
        if path == "/proxy":
            await proxy(scope, receive, send, q)
        elif path == "/tproxy":
            await tproxy(scope, receive, send, q)
        elif path == "/stats":
            with _lock:
                snapshot = dict(stats)
            await respond_json(send, snapshot)
        elif path == "/logs":
            await respond_json(send, list(log_records))
        elif path == "/db":
            await db(scope, receive, send, q)
        elif path in ("/file", "/upload"):
            await files(scope, receive, send, q)
        elif path == "/dns":
            await dns(scope, receive, send, q)
        elif path == "/proc":
            await proc(scope, receive, send, q)
        else:
            await respond(send, 404, b"no route")
    except NotImplementedError as e:
        await respond(send, 501, ("NotImplementedError: %s" % e).encode())
`

// upstream is the Go HTTP server the handlers proxy to.
type upstream struct {
	*httptest.Server
	host                  string // 127.0.0.1:port
	inflight, maxInflight atomic.Int64
	ended                 chan string // a /forever response ended, and why
	mu                    sync.Mutex
	groups                map[string]*barrierGroup
}

type barrierGroup struct {
	n, arrived int
	met        chan struct{}
}

// payload is n deterministic bytes.
func payload(n int, seed uint64) []byte {
	var key [32]byte
	key[0], key[1] = byte(seed), byte(seed>>8)
	b := make([]byte, n)
	rand.NewChaCha8(key).Read(b)
	return b
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{ended: make(chan string, 16), groups: map[string]*barrierGroup{}}
	mux := http.NewServeMux()
	// /data?n=&seed=[&status=][&chunked=1]: n bytes, with a Content-Length or chunked.
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, _ := strconv.Atoi(q.Get("n"))
		seed, _ := strconv.ParseUint(q.Get("seed"), 10, 64)
		status := 200
		if s := q.Get("status"); s != "" {
			status, _ = strconv.Atoi(s)
		}
		b := payload(n, seed)
		u.head(w, r, b)
		chunked := q.Get("chunked") == "1"
		if !chunked {
			w.Header().Set("Content-Length", strconv.Itoa(n))
		}
		w.WriteHeader(status)
		rc := http.NewResponseController(w)
		for len(b) > 0 {
			k := min(len(b), 32<<10)
			if _, err := w.Write(b[:k]); err != nil {
				return
			}
			b = b[k:]
			if chunked {
				rc.Flush()
			}
		}
	})
	// /echo: the request body back.
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.head(w, r, b)
		w.Write(b)
	})
	// /slow?d=: answers after d, counting the requests in flight.
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		n := u.inflight.Add(1)
		for m := u.maxInflight.Load(); n > m && !u.maxInflight.CompareAndSwap(m, n); m = u.maxInflight.Load() {
		}
		d, _ := time.ParseDuration(r.URL.Query().Get("d"))
		time.Sleep(d)
		u.inflight.Add(-1)
		io.WriteString(w, "slow ok")
	})
	// /barrier?key=&n=&wait=: answers "met" once n requests of key are in flight together,
	// or 504 after wait. Only concurrent requests can pass.
	mux.HandleFunc("/barrier", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, _ := strconv.Atoi(q.Get("n"))
		wait, _ := time.ParseDuration(q.Get("wait"))
		u.mu.Lock()
		g := u.groups[q.Get("key")]
		if g == nil {
			g = &barrierGroup{n: n, met: make(chan struct{})}
			u.groups[q.Get("key")] = g
		}
		if g.arrived++; g.arrived == g.n {
			close(g.met)
		}
		u.mu.Unlock()
		select {
		case <-g.met:
			io.WriteString(w, "met")
		case <-time.After(wait):
			u.mu.Lock()
			a := g.arrived
			u.mu.Unlock()
			http.Error(w, fmt.Sprintf("barrier %s: %d of %d requests arrived in %v", q.Get("key"), a, n, wait), 504)
		}
	})
	// /forever: 16 KiB every 5 ms until the client goes (or 20 s pass).
	mux.HandleFunc("/forever", func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("f"), 16<<10)
		rc := http.NewResponseController(w)
		deadline := time.After(20 * time.Second)
		rc.SetWriteDeadline(time.Now().Add(20 * time.Second)) // a client that stops reading
		for {
			select {
			case <-r.Context().Done():
				u.ended <- "client closed"
				return
			case <-deadline:
				u.ended <- "still open after 20 s"
				return
			case <-time.After(5 * time.Millisecond):
			}
			if _, err := w.Write(chunk); err != nil {
				u.ended <- "client closed (write: " + err.Error() + ")"
				return
			}
			rc.Flush()
		}
	})
	u.Server = httptest.NewServer(mux)
	t.Cleanup(u.Close)
	u.host = strings.TrimPrefix(u.URL, "http://")
	return u
}

func (u *upstream) head(w http.ResponseWriter, r *http.Request, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Up-Sha256", sum(body))
	h.Set("X-Up-Method", r.Method)
	h.Set("X-Up-Fwd", r.Header.Get("X-Fwd"))
	h.Add("Set-Cookie", "a=1; Path=/")
	h.Add("Set-Cookie", "b=2; Path=/")
}

func loopThreadMode() bool { return os.Getenv("PYTHAGO_ASGI_LOOP_THREAD") == "1" }

// ioServer serves ioapp with two workers, inline or (PYTHAGO_ASGI_LOOP_THREAD=1) on the
// loop threads.
func ioServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	s, err := asgi.New("ioapp:app", asgi.Options{Workers: 2, LoopThread: loopThreadMode()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	c := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}, Timeout: 60 * time.Second}
	t.Cleanup(func() {
		c.CloseIdleConnections()
		ts.Close()
		s.Close(context.Background())
	})
	return ts, c
}

func call(c *http.Client, method, url string, body []byte) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

func mustCall(t *testing.T, c *http.Client, method, url string, body []byte, v any) []byte {
	t.Helper()
	resp, b, err := call(c, method, url, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("%s %s: %d %s", method, url, resp.StatusCode, b)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s %s: %v: %s", method, url, err, b)
		}
	}
	return b
}

func proxyURL(base, route, up, p string) string {
	return base + route + "&up=" + url.QueryEscape(up) + "&p=" + url.QueryEscape(p)
}

var proxyRoutes = []string{"/proxy?via=streams", "/tproxy?mode=thread", "/tproxy?mode=executor"}

// TestIOProxy relays upstream responses (status, headers, body) through asyncio streams
// on the worker's loop, through urllib in asyncio.to_thread and through urllib reads in
// run_in_executor: bodies of 0 B to 4 MiB, Content-Length and chunked, an error status,
// and a 1 MiB request body.
func TestIOProxy(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	upload := payload(1<<20, 3)
	cases := []struct {
		name, method, path string
		status             int
		body               []byte
	}{
		{"empty", "GET", "/data?n=0&seed=1", 200, nil},
		{"small", "GET", "/data?n=1000&seed=1", 200, payload(1000, 1)},
		{"4MiB", "GET", "/data?n=4194304&seed=2", 200, payload(4<<20, 2)},
		{"3MiB-chunked", "GET", "/data?n=3145733&seed=4&chunked=1", 200, payload(3<<20+5, 4)},
		{"404", "GET", "/data?n=100&seed=5&status=404", 404, payload(100, 5)},
		{"post-1MiB", "POST", "/echo", 200, upload},
	}
	for _, route := range proxyRoutes {
		fwd := map[string]string{"/proxy?via=streams": "asgi-streams", "/tproxy?mode=thread": "thread", "/tproxy?mode=executor": "executor"}[route]
		for _, tc := range cases {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				var req []byte
				if tc.method == "POST" {
					req = upload
				}
				resp, b, err := call(c, tc.method, proxyURL(ts.URL, route, u.host, tc.path), req)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != tc.status {
					t.Fatalf("status %d, want %d: %.200s", resp.StatusCode, tc.status, b)
				}
				if !bytes.Equal(b, tc.body) || resp.Header.Get("X-Up-Sha256") != sum(b) {
					t.Errorf("body: %d bytes, sha256 %s; want %d bytes, upstream says %s",
						len(b), sum(b), len(tc.body), resp.Header.Get("X-Up-Sha256"))
				}
				if got := resp.Header.Values("Set-Cookie"); !slices.Equal(got, []string{"a=1; Path=/", "b=2; Path=/"}) {
					t.Errorf("Set-Cookie relayed as %q", got)
				}
				if m, f := resp.Header.Get("X-Up-Method"), resp.Header.Get("X-Up-Fwd"); m != tc.method || f != fwd {
					t.Errorf("the upstream saw method %q, X-Fwd %q; want %q, %q", m, f, tc.method, fwd)
				}
			})
		}
	}
}

// TestIOProxyConcurrent: requests waiting on the upstream do not hold the worker. 16
// requests on two workers must all be at the upstream at once (a barrier only they can
// pass together) when they wait on the loop (asyncio streams). In executor threads, the
// default executor has min(32, cpu+4) threads per loop, and requests need not split
// evenly between the workers: one executor's worth always fits, however they split
// (9 of 16 on one worker with 8 threads left the barrier one short on a 4-CPU runner).
// And 16 requests to an upstream that takes 250 ms are there together: as many as the
// barrier takes at once, which a serialized server never has. Their time is logged, not
// bounded (GitHub's Intel Macs took from 0.6 to 3.5 s).
func TestIOProxyConcurrent(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	const n = 16
	run := func(n int, p func(i int) string) (time.Duration, []string) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var errs []string
		start := time.Now()
		for i := range n {
			wg.Go(func() {
				resp, b, err := call(c, "GET", p(i), nil)
				if err == nil && resp.StatusCode != 200 {
					err = fmt.Errorf("%d %s", resp.StatusCode, b)
				}
				if err != nil {
					mu.Lock()
					errs = append(errs, err.Error())
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		return time.Since(start), errs
	}
	var logs []string
	mustCall(t, c, "GET", ts.URL+"/logs", nil, &logs)
	for _, route := range proxyRoutes {
		key := fmt.Sprintf("%s-%d", route, time.Now().UnixNano())
		meet := n
		if strings.HasPrefix(route, "/tproxy") {
			meet = min(n, min(32, runtime.NumCPU()+4)) // one worker's executor threads
		}
		d, errs := run(meet, func(int) string {
			return proxyURL(ts.URL, route, u.host, fmt.Sprintf("/barrier?n=%d&wait=5s&key=%s", meet, url.QueryEscape(key)))
		})
		if len(errs) > 0 {
			t.Errorf("%s: %d of %d requests failed to meet at the upstream: %s", route, len(errs), meet, errs[0])
		}
		logs = dumpLogs(t, c, ts.URL, logs)
		const delay = 250 * time.Millisecond
		u.maxInflight.Store(0)
		slow, errs2 := run(n, func(int) string { return proxyURL(ts.URL, route, u.host, "/slow?d=250ms") })
		if len(errs2) > 0 {
			t.Errorf("%s /slow: %d of %d failed: %s", route, len(errs2), n, errs2[0])
		}
		logs = dumpLogs(t, c, ts.URL, logs)
		if m := u.maxInflight.Load(); m < int64(meet) {
			t.Errorf("%s: %d requests to a %v upstream: at most %d at the upstream at once, want %d (took %v)",
				route, n, delay, m, meet, slow)
		}
		t.Logf("%s: barrier of %d in %v; %d × %v upstream in %v, %d at once", route, meet, d, n, delay, slow, u.maxInflight.Load())
	}
}

// dumpLogs reports what the server logged (pythago.asgi, with tracebacks) after the first
// len(seen) records, and returns them all.
func dumpLogs(t *testing.T, c *http.Client, base string, seen []string) []string {
	t.Helper()
	var logs []string
	mustCall(t, c, "GET", base+"/logs", nil, &logs)
	for _, l := range logs[min(len(seen), len(logs)):] {
		t.Errorf("the server logged: %s", l)
	}
	return logs
}

// TestIOProxyGC: a request waiting in reader.readline() for the upstream's response
// survives a garbage collection. asyncio.StreamReaderProtocol holds its StreamReader by a
// weakref, so only the request's task holds the future readline() waits on, and asyncio
// holds tasks weakly: the server must keep the task (TestTaskKeptAlive). Otherwise the
// collection frees it, its coroutine gets GeneratorExit and the client a 500, which is
// how TestIOProxyConcurrent failed when a collection ran during its barrier.
func TestIOProxyGC(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	var logs []string
	mustCall(t, c, "GET", ts.URL+"/logs", nil, &logs)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			resp, b, err := call(c, "GET", proxyURL(ts.URL, "/proxy?via=streams&gc=0.05", u.host, "/slow?d=200ms"), nil)
			if err == nil && (resp.StatusCode != 200 || string(b) != "slow ok") {
				err = fmt.Errorf("%d %q", resp.StatusCode, b)
			}
			if err != nil {
				t.Errorf("a relay across gc.collect(): %v", err)
			}
		})
	}
	wg.Wait()
	dumpLogs(t, c, ts.URL, logs)
}

func stats(t *testing.T, c *http.Client, base string) (s struct{ Active, Opened, Closed int }) {
	t.Helper()
	mustCall(t, c, "GET", base+"/stats", nil, &s)
	return s
}

// disconnectMidProxy starts a relay of the endless /forever, reads 256 KiB and goes away;
// the upstream must see its connection end within 5 s.
func disconnectMidProxy(t *testing.T, c *http.Client, base string, u *upstream) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", proxyURL(base, "/proxy?via=streams", u.host, "/forever"), nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 256<<10)); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	select {
	case why := <-u.ended:
		if !strings.HasPrefix(why, "client closed") {
			t.Fatalf("upstream response ended: %s", why)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream connection was still open 5 s after the client went away")
	}
}

// waitRelays waits until no relay is active and closed reached want.
func waitRelays(t *testing.T, c *http.Client, base string, active, closed int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := stats(t, c, base)
		if s.Active == active && s.Closed == closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("relays after the clients went away: %+v, want %d active, %d closed", s, active, closed)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestIOProxyDisconnect: a client that goes away in the middle of an endless upstream
// response ends the relay. The application sees http.disconnect while it awaits the
// upstream, closes the upstream connection (the upstream sees it), and the server keeps
// serving.
func TestIOProxyDisconnect(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	before := stats(t, c, ts.URL)
	for range 3 {
		disconnectMidProxy(t, c, ts.URL, u)
	}
	waitRelays(t, c, ts.URL, before.Active, before.Closed+3)
	resp, b, err := call(c, "GET", proxyURL(ts.URL, "/proxy?via=streams", u.host, "/data?n=5000&seed=8"), nil)
	if err != nil || resp.StatusCode != 200 || !bytes.Equal(b, payload(5000, 8)) {
		t.Fatalf("after the disconnects: %v %v %d bytes", err, resp, len(b))
	}
}

// TestIOProxyDisconnectQuiet: an application that returns after http.disconnect, without
// finishing the response it can no longer send, is not an error. uvicorn logs nothing
// (httptools_impl.py: "not self.response_complete and not self.disconnected").
func TestIOProxyDisconnectQuiet(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	var logs0 []string
	mustCall(t, c, "GET", ts.URL+"/logs", nil, &logs0)
	before := stats(t, c, ts.URL)
	disconnectMidProxy(t, c, ts.URL, u)
	waitRelays(t, c, ts.URL, before.Active, before.Closed+1)
	var logs []string
	mustCall(t, c, "GET", ts.URL+"/logs", nil, &logs)
	if len(logs) > len(logs0) {
		t.Errorf("after a client disconnect the server logged %q", logs[len(logs0):])
	}
}

// TestIOSQLite: concurrent writers and readers on one on-disk WAL database, blocking on
// the loop (or on the request's thread, inline) and in asyncio.to_thread. Each write is
// one transaction of 50 rows and a read-modify-write of a counter; readers must see whole
// transactions only, and nothing may be lost. A transaction that raises is rolled back
// and does not keep the database locked.
func TestIOSQLite(t *testing.T) {
	ts, c := ioServer(t)
	dbPath := filepath.Join(t.TempDir(), "io.db")
	q := "&db=" + url.QueryEscape(dbPath)
	mustCall(t, c, "GET", ts.URL+"/db?op=init"+q, nil, nil)
	const writers, rows = 16, 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []string
	fail := func(f string, a ...any) {
		mu.Lock()
		errs = append(errs, fmt.Sprintf(f, a...))
		mu.Unlock()
	}
	for i := range writers {
		mode := []string{"direct", "thread"}[i%2]
		wg.Go(func() {
			resp, b, err := call(c, "GET", fmt.Sprintf("%s/db?op=write&w=%d&n=%d&mode=%s%s", ts.URL, i, rows, mode, q), nil)
			if err != nil || resp.StatusCode != 200 {
				fail("writer %d (%s): %v %v %s", i, mode, err, resp, b)
			}
		})
		wg.Go(func() {
			var r struct{ Count, Counter int }
			resp, b, err := call(c, "GET", fmt.Sprintf("%s/db?op=read&mode=%s%s", ts.URL, mode, q), nil)
			if err == nil && resp.StatusCode == 200 {
				err = json.Unmarshal(b, &r)
			}
			if err != nil || resp.StatusCode != 200 {
				fail("reader (%s): %v %v %s", mode, err, resp, b)
			} else if r.Count%rows != 0 || r.Counter != r.Count {
				fail("reader (%s) saw part of a transaction: %d rows, counter %d", mode, r.Count, r.Counter)
			}
		})
	}
	wg.Wait()
	for _, e := range errs {
		t.Error(e)
	}
	var r struct{ Count, Counter int }
	mustCall(t, c, "GET", ts.URL+"/db?op=read"+q, nil, &r)
	if r.Count != writers*rows || r.Counter != writers*rows {
		t.Errorf("after %d writers of %d rows: %d rows, counter %d", writers, rows, r.Count, r.Counter)
	}
	// A transaction that raises: rolled back, the response is a 500, and the next writer
	// gets the lock at once.
	for _, mode := range []string{"direct", "thread"} {
		resp, _, err := call(c, "GET", ts.URL+"/db?op=crash&w=99&n=10&mode="+mode+q, nil)
		if err != nil || resp.StatusCode != 500 {
			t.Errorf("crash (%s): %v %v", mode, err, resp)
		}
		start := time.Now()
		mustCall(t, c, "GET", ts.URL+"/db?op=write&w=100&n=1&mode="+mode+q, nil, nil)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("a write after the crash (%s) waited %v for the lock", mode, d)
		}
	}
	mustCall(t, c, "GET", ts.URL+"/db?op=read"+q, nil, &r)
	if want := writers*rows + 2; r.Count != want || r.Counter != want {
		t.Errorf("after the crashed transactions: %d rows, counter %d; want %d", r.Count, r.Counter, want)
	}
}

// TestIOSQLiteThreadAffinity checks the documented cost of inline requests: a sqlite3
// connection (check_same_thread=True, the default) opened before an await the loop must
// complete and used after it fails exactly when the request changed threads. With
// Options.LoopThread a request never changes threads, as under uvicorn; inline, a
// request alone on its worker starts on its goroutine's thread and continues on the loop
// thread. check_same_thread=False works in both modes. The await is a timer, or send()
// waiting for the client past the high-water mark (a 2 MiB streamed response).
func TestIOSQLiteThreadAffinity(t *testing.T) {
	ts, c := ioServer(t)
	dbPath := filepath.Join(t.TempDir(), "affinity.db")
	q := "&db=" + url.QueryEscape(dbPath)
	mustCall(t, c, "GET", ts.URL+"/db?op=init"+q, nil, nil)
	const notSame = "SQLite objects created in a thread can only be used in that same thread"
	for _, aw := range []string{"sleep", "send"} {
		for _, cst := range []string{"1", "0"} {
			b := mustCall(t, c, "GET", ts.URL+"/db?op=affinity&await="+aw+"&cst="+cst+q, nil, nil)
			if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
				b = b[i+1:]
			}
			var r struct {
				Threads int
				Errors  []string
			}
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatalf("%s: %v: %.200s", aw, err, b)
			}
			switch {
			case loopThreadMode() && r.Threads != 1:
				t.Errorf("await %s, LoopThread: the request ran on %d threads", aw, r.Threads)
			case !loopThreadMode() && aw == "sleep" && r.Threads != 2:
				t.Errorf("await sleep, inline: the request ran on %d threads, want 2 (inline, then the loop thread)", r.Threads)
			case cst == "0" && len(r.Errors) > 0:
				t.Errorf("await %s, check_same_thread=False: %q", aw, r.Errors[0])
			case cst == "1" && (r.Threads > 1) != (len(r.Errors) > 0):
				t.Errorf("await %s: %d threads but %d same-thread errors", aw, r.Threads, len(r.Errors))
			case len(r.Errors) > 0 && !strings.Contains(r.Errors[0], notSame):
				t.Errorf("await %s: %q", aw, r.Errors[0])
			}
			t.Logf("await %s, check_same_thread=%s: %d threads, %d errors", aw, cst, r.Threads, len(r.Errors))
		}
	}
}

// TestIOFiles reads an 8 MiB file in chunks into a streamed response, and writes a 5 MiB
// streamed request body into a file, blocking on the request's thread and in
// asyncio.to_thread.
func TestIOFiles(t *testing.T) {
	ts, c := ioServer(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	data := payload(8<<20+7, 11)
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	upload := payload(5<<20+3, 12)
	for _, mode := range []string{"direct", "thread"} {
		b := mustCall(t, c, "GET", ts.URL+"/file?chunk=1000000&mode="+mode+"&path="+url.QueryEscape(src), nil, nil)
		if !bytes.Equal(b, data) {
			t.Errorf("%s read: %d bytes, sha256 %s; want %d, %s", mode, len(b), sum(b), len(data), sum(data))
		}
		dst := filepath.Join(dir, mode+".bin")
		var r struct {
			Sha256 string
			Len    int
		}
		mustCall(t, c, "POST", ts.URL+"/upload?mode="+mode+"&path="+url.QueryEscape(dst), upload, &r)
		got, err := os.ReadFile(dst)
		if err != nil || !bytes.Equal(got, upload) || r.Sha256 != sum(upload) || r.Len != len(upload) {
			t.Errorf("%s write: %v, file %d bytes, the app hashed %d bytes as %s; want %d, %s",
				mode, err, len(got), r.Len, r.Sha256, len(upload), sum(upload))
		}
	}
}

// TestIODNS resolves localhost with the loop's resolver (Go's, GoEventLoop.getaddrinfo)
// and with libc's getaddrinfo (stock asyncio's, in an executor thread), which must
// agree, and connects to the upstream by name.
func TestIODNS(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	var r struct {
		Go, Libc []string
		Status   int
		Sha256   string
	}
	mustCall(t, c, "GET", ts.URL+"/dns?up="+url.QueryEscape(u.host), nil, &r)
	if !slices.Equal(r.Go, r.Libc) || !slices.Contains(r.Go, "127.0.0.1") {
		t.Errorf("localhost: the loop resolved %q, libc %q", r.Go, r.Libc)
	}
	if r.Status != 200 || r.Sha256 != sum(payload(100000, 9)) {
		t.Errorf("GET via localhost: %d %s", r.Status, r.Sha256)
	}
	t.Logf("localhost: loop %q, libc %q", r.Go, r.Libc)
}

// TestIOSubprocess runs a shell that copies 1 MiB of stdin to stdout with
// subprocess.run in asyncio.to_thread, 8 requests at once.
func TestIOSubprocess(t *testing.T) {
	ts, c := ioServer(t)
	in := payload(1<<20, 13)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			x := strconv.Itoa(i)
			resp, b, err := call(c, "POST", ts.URL+"/proc?mode=thread&x="+x, in)
			if want := append(slices.Clip(in), "done-"+x+"\n"...); err != nil || resp.StatusCode != 200 ||
				resp.Header.Get("X-Rc") != "0" || !bytes.Equal(b, want) {
				t.Errorf("subprocess %d: %v %v, %d bytes (want %d)", i, err, resp, len(b), len(want))
			}
		})
	}
	wg.Wait()
}

// TestIOSubprocessAsyncio runs the same shell with asyncio.create_subprocess_exec on the
// worker's loop, as an application under uvicorn can.
func TestIOSubprocessAsyncio(t *testing.T) {
	ts, c := ioServer(t)
	resp, b, err := call(c, "POST", ts.URL+"/proc?mode=aio&x=a", []byte("hello\n"))
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("X-Rc") != "0" || string(b) != "hello\ndone-a\n" {
		t.Fatalf("%v %v %q", err, resp, b)
	}
}
