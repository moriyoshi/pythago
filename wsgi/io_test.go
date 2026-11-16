package wsgi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
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

	"github.com/moriyoshi/pythago/wsgi"
)

// ioapp does real, blocking I/O in WSGI handlers, on the thread serving the request: an
// HTTP proxy over http.client and urllib, sqlite3, files, name resolution and
// subprocesses.
const ioapp = `
import hashlib, http.client, json, os, socket, sqlite3, subprocess, sys, threading, time
import urllib.error, urllib.request
from urllib.parse import parse_qs

HOP = {"connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade", "proxy-connection"}
_lock = threading.Lock()
stats = {"active": 0, "opened": 0, "closed": 0}

def count(key, d):
    with _lock:
        stats[key] += d

def query(environ):
    return {k: v[0] for k, v in parse_qs(environ.get("QUERY_STRING", "")).items()}

def read_body(environ):
    n = int(environ.get("CONTENT_LENGTH") or 0)
    f, parts = environ["wsgi.input"], []
    while n > 0:
        b = f.read(min(n, 65536))
        if not b:
            break
        parts.append(b)
        n -= len(b)
    return b"".join(parts)

def respond_json(start_response, obj, status="200 OK"):
    start_response(status, [("Content-Type", "application/json")])
    return [json.dumps(obj).encode()]

class Relay:
    """Streams an http.client response; close() (which the server always calls, also
    when the client went away) closes the upstream connection."""
    def __init__(self, conn, resp):
        self.conn, self.resp = conn, resp
        count("active", 1)
        count("opened", 1)

    def __iter__(self):
        while True:
            b = self.resp.read(65536)
            if not b:
                return
            yield b

    def close(self):
        self.conn.close()
        count("active", -1)
        count("closed", 1)

def proxy(environ, start_response, q):
    host, port = q["up"].rsplit(":", 1)
    conn = http.client.HTTPConnection(host, int(port), timeout=30)
    method = environ["REQUEST_METHOD"]
    conn.request(method, q["p"], body=read_body(environ) if method == "POST" else None,
                 headers={"X-Fwd": "http.client"})
    resp = conn.getresponse()
    start_response("%d %s" % (resp.status, resp.reason),
                   [(k, v) for k, v in resp.getheaders() if k.lower() not in HOP])
    return Relay(conn, resp)

def uproxy(environ, start_response, q):
    method = environ["REQUEST_METHOD"]
    req = urllib.request.Request("http://%s%s" % (q["up"], q["p"]), method=method,
                                 data=read_body(environ) if method == "POST" else None,
                                 headers={"X-Fwd": "urllib"})
    try:
        r = urllib.request.urlopen(req, timeout=30)
    except urllib.error.HTTPError as e:
        r = e
    with r:
        body = r.read()
        start_response("%d %s" % (r.getcode(), r.reason),
                       [(k, v) for k, v in r.headers.items() if k.lower() not in HOP])
    return [body]

# sqlite3
def db_connect(path, timeout=20.0, **kw):
    return sqlite3.connect(path, timeout=timeout, isolation_level=None, **kw)

def db_write(c, writer, n, crash=False):
    """One transaction: n rows and a read-modify-write of the counter."""
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

_tl = threading.local()
_holding = threading.Event()  # op=hold has the write lock

def thread_conn(path):
    """A connection per thread (check_same_thread=True), as per-thread pools keep them."""
    conns = getattr(_tl, "conns", None)
    if conns is None:
        conns = _tl.conns = {}
    if path not in conns:
        conns[path] = db_connect(path)
    return conns[path]

class Rows:
    """Streams 64 KiB chunks, querying the same connection before each one; the
    server writes each chunk (detached) between them."""
    def __init__(self, c, n):
        self.c, self.n, self.errors, self.threads = c, n, [], set()

    def __iter__(self):
        for i in range(self.n):
            self.threads.add(threading.get_ident())
            try:
                self.c.execute("SELECT count(*) FROM t").fetchone()
            except sqlite3.ProgrammingError as e:
                self.errors.append(str(e))
            yield b"x" * 65535 + b"\n"
        yield json.dumps({"threads": len(self.threads), "errors": self.errors}).encode()

def db(environ, start_response, q):
    op, path = q["op"], q["db"]
    if op == "init":
        c = db_connect(path)
        c.execute("PRAGMA journal_mode=WAL")
        c.execute("CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, writer INTEGER, seq INTEGER, payload BLOB)")
        c.execute("CREATE TABLE IF NOT EXISTS counter (k TEXT PRIMARY KEY, v INTEGER)")
        c.execute("INSERT OR IGNORE INTO counter VALUES ('rows', 0)")
        c.close()
        return respond_json(start_response, {})
    if op in ("write", "crash"):
        c = thread_conn(path) if q.get("conn") == "thread" else db_connect(path, float(q.get("timeout", 20)))
        try:
            db_write(c, int(q["w"]), int(q["n"]), op == "crash")
        except sqlite3.OperationalError as e:
            return respond_json(start_response, {"error": str(e)}, "503 Service Unavailable")
        finally:
            if q.get("conn") != "thread":
                c.close()
        return respond_json(start_response, {})
    if op == "read":
        c = db_connect(path)
        try:
            c.execute("BEGIN")
            (n,) = c.execute("SELECT count(*) FROM t").fetchone()
            (v,) = c.execute("SELECT v FROM counter WHERE k = 'rows'").fetchone()
            c.execute("COMMIT")
        finally:
            c.close()
        return respond_json(start_response, {"count": n, "counter": v})
    if op == "hold":  # the write lock, for ms milliseconds; op=held reports it taken
        c = db_connect(path)
        try:
            c.execute("BEGIN IMMEDIATE")
            _holding.set()
            time.sleep(int(q["ms"]) / 1000)
            c.execute("COMMIT")
        finally:
            _holding.clear()
            c.close()
        return respond_json(start_response, {})
    if op == "held":
        return respond_json(start_response, {"held": _holding.is_set()})
    if op == "stream":
        start_response("200 OK", [("Content-Type", "application/octet-stream")])
        return Rows(thread_conn(path), int(q["n"]))

def files(environ, start_response, q):
    if environ["PATH_INFO"] == "/file":
        chunk = int(q.get("chunk", 1 << 20))
        f = open(q["path"], "rb")
        start_response("200 OK", [("Content-Type", "application/octet-stream")])
        if q.get("mode") == "wrapper":
            return environ["wsgi.file_wrapper"](f, chunk)
        def gen():
            with f:
                while b := f.read(chunk):
                    yield b
        return gen()
    h, n, left = hashlib.sha256(), 0, int(environ.get("CONTENT_LENGTH") or 0)
    with open(q["path"], "wb") as f:
        while left > 0:
            b = environ["wsgi.input"].read(min(left, 1 << 20))
            if not b:
                break
            f.write(b)
            h.update(b)
            n += len(b)
            left -= len(b)
    return respond_json(start_response, {"sha256": h.hexdigest(), "len": n})

def dns(environ, start_response, q):
    port = int(q["up"].rsplit(":", 1)[1])
    addrs = sorted({a[4][0] for a in socket.getaddrinfo("localhost", port, type=socket.SOCK_STREAM)})
    conn = http.client.HTTPConnection("localhost", port, timeout=10)
    try:
        conn.request("GET", "/data?n=100000&seed=9")
        r = conn.getresponse()
        status, body, error = r.status, r.read(), ""
    except OSError as e:  # reported, for the test to tell a resolver without ::1 apart
        status, body, error = 0, b"", repr(e)
    finally:
        conn.close()
    return respond_json(start_response, {"addrs": addrs, "status": status, "error": error,
                                         "sha256": hashlib.sha256(body).hexdigest()})

COMMAND = ["/bin/sh", "-c", "cat; echo done-$PYTHAGO_X"]
if sys.platform == "win32":  # no sh: the same in Python (this test binary)
    COMMAND = [sys.executable, "-c", "import os, shutil, sys; "
               "shutil.copyfileobj(sys.stdin.buffer, sys.stdout.buffer); "
               "sys.stdout.buffer.write(('done-' + os.environ['PYTHAGO_X'] + chr(10)).encode())"]

def proc(environ, start_response, q):
    r = subprocess.run(COMMAND, input=read_body(environ),
                       capture_output=True, env=dict(os.environ, PYTHAGO_X=q.get("x", "")), timeout=20)
    start_response("200 OK", [("Content-Type", "application/octet-stream"), ("X-Rc", str(r.returncode))])
    return [r.stdout]

def app(environ, start_response):
    q, path = query(environ), environ["PATH_INFO"]
    if path == "/proxy":
        return proxy(environ, start_response, q)
    if path == "/uproxy":
        return uproxy(environ, start_response, q)
    if path == "/stats":
        with _lock:
            snapshot = dict(stats)
        return respond_json(start_response, snapshot)
    if path == "/db":
        return db(environ, start_response, q)
    if path in ("/file", "/upload"):
        return files(environ, start_response, q)
    if path == "/dns":
        return dns(environ, start_response, q)
    if path == "/proc":
        return proc(environ, start_response, q)
    start_response("404 Not Found", [])
    return [b"no route"]
`

// upstream is the Go HTTP server the handlers proxy to.
type upstream struct {
	*httptest.Server
	host                  string // 127.0.0.1:port, or [::1]:port (httptest's fallback)
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
	h.Set("X-Up-Sha256", sha(body))
	h.Set("X-Up-Method", r.Method)
	h.Set("X-Up-Fwd", r.Header.Get("X-Fwd"))
	h.Add("Set-Cookie", "a=1; Path=/")
	h.Add("Set-Cookie", "b=2; Path=/")
}

func ioServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	s, err := wsgi.New("ioapp:app", wsgi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	c := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}, Timeout: 60 * time.Second}
	t.Cleanup(func() {
		c.CloseIdleConnections()
		ts.Close()
		s.Close()
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
	return base + route + "?up=" + url.QueryEscape(up) + "&p=" + url.QueryEscape(p)
}

// concurrently runs n GETs of p(i) at once and returns how long they took and what failed.
func concurrently(c *http.Client, n int, p func(i int) string) (time.Duration, []string) {
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

// TestIOProxy relays upstream responses (status, headers, body) with http.client
// (streamed: the result is an iterable whose close() closes the upstream connection) and
// with urllib (read whole): bodies of 0 B to 4 MiB, Content-Length and chunked, an error
// status, and a 1 MiB request body.
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
	for _, route := range []string{"/proxy", "/uproxy"} {
		fwd := map[string]string{"/proxy": "http.client", "/uproxy": "urllib"}[route]
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
				if !bytes.Equal(b, tc.body) || resp.Header.Get("X-Up-Sha256") != sha(b) {
					t.Errorf("body: %d bytes, sha256 %s; want %d bytes, upstream says %s",
						len(b), sha(b), len(tc.body), resp.Header.Get("X-Up-Sha256"))
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

// TestIOProxyConcurrent: a request blocked on a socket holds its admission slot
// (py.Config.Slots, the server's thread pool) and nothing else. Slots requests must all
// be at the upstream at once (a barrier only they can pass together); twice as many to
// an upstream that takes 250 ms run in two waves, never more than Slots at the upstream.
func TestIOProxyConcurrent(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	for _, route := range []string{"/proxy", "/uproxy"} {
		key := fmt.Sprintf("%s-%d", route, time.Now().UnixNano())
		d, errs := concurrently(c, slots, func(int) string {
			return proxyURL(ts.URL, route, u.host, fmt.Sprintf("/barrier?n=%d&wait=5s&key=%s", slots, url.QueryEscape(key)))
		})
		if len(errs) > 0 {
			t.Errorf("%s: %d of %d requests failed to meet at the upstream: %s", route, len(errs), slots, errs[0])
		}
		const delay = 250 * time.Millisecond
		u.maxInflight.Store(0)
		slow, errs := concurrently(c, 2*slots, func(int) string { return proxyURL(ts.URL, route, u.host, "/slow?d=250ms") })
		if len(errs) > 0 {
			t.Errorf("%s /slow: %s", route, errs[0])
		}
		if m := u.maxInflight.Load(); m != slots {
			t.Errorf("%s: %d requests at most at the upstream at once, want the %d slots", route, m, slots)
		}
		if slow < 2*delay || slow > 6*delay {
			t.Errorf("%s: %d requests to a %v upstream on %d slots took %v", route, 2*slots, delay, slots, slow)
		}
		t.Logf("%s: barrier of %d in %v; %d × %v upstream in %v", route, slots, d, 2*slots, delay, slow)
	}
}

func relayStats(t *testing.T, c *http.Client, base string) (s struct{ Active, Opened, Closed int }) {
	t.Helper()
	mustCall(t, c, "GET", base+"/stats", nil, &s)
	return s
}

// TestIOProxyDisconnect: a client that goes away in the middle of an endless upstream
// response ends the relay: the server's next write fails, it calls the result's close(),
// which closes the upstream connection (the upstream sees it), and the slot is free again.
func TestIOProxyDisconnect(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	before := relayStats(t, c, ts.URL)
	const rounds = slots + 2 // a leaked slot would starve the last rounds
	for round := range rounds {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", proxyURL(ts.URL, "/proxy", u.host, "/forever"), nil)
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
				t.Fatalf("round %d: upstream response ended: %s", round, why)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: the upstream connection was still open 5 s after the client went away", round)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := relayStats(t, c, ts.URL)
		if s.Active == before.Active && s.Closed == before.Closed+rounds {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("relays after the clients went away: before %+v, now %+v", before, s)
		}
		time.Sleep(10 * time.Millisecond)
	}
	d, errs := concurrently(c, slots, func(int) string {
		return proxyURL(ts.URL, "/proxy", u.host, fmt.Sprintf("/barrier?n=%d&wait=5s&key=after-disconnect", slots))
	})
	if len(errs) > 0 {
		t.Errorf("after the disconnects, %d of %d slots were usable: %s (%v)", slots-len(errs), slots, errs[0], d)
	}
}

// TestIOSQLite: concurrent writers and readers on one on-disk WAL database. Each write
// is one transaction of 50 rows and a read-modify-write of a counter; readers must see
// whole transactions only, and nothing may be lost. A transaction that raises is rolled
// back and does not keep the database locked; a writer that finds it locked waits for
// its busy timeout and then gets "database is locked", as sqlite3 does anywhere.
func TestIOSQLite(t *testing.T) {
	ts, c := ioServer(t)
	dbPath := filepath.Join(dbDir(t), "io.db")
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
		conn := []string{"request", "thread"}[i%2] // a connection per request, or per thread
		wg.Go(func() {
			resp, b, err := call(c, "GET", fmt.Sprintf("%s/db?op=write&w=%d&n=%d&conn=%s%s", ts.URL, i, rows, conn, q), nil)
			if err != nil || resp.StatusCode != 200 {
				fail("writer %d (%s): %v %v %s", i, conn, err, resp, b)
			}
		})
		wg.Go(func() {
			var r struct{ Count, Counter int }
			resp, b, err := call(c, "GET", ts.URL+"/db?op=read"+q, nil)
			if err == nil && resp.StatusCode == 200 {
				err = json.Unmarshal(b, &r)
			}
			if err != nil || resp.StatusCode != 200 {
				fail("reader: %v %v %s", err, resp, b)
			} else if r.Count%rows != 0 || r.Counter != r.Count {
				fail("reader saw part of a transaction: %d rows, counter %d", r.Count, r.Counter)
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
	// A transaction that raises: rolled back, a 500, and the next writer gets the lock.
	for _, conn := range []string{"request", "thread"} {
		resp, _, err := call(c, "GET", ts.URL+"/db?op=crash&w=99&n=10&conn="+conn+q, nil)
		if err != nil || resp.StatusCode != 500 {
			t.Errorf("crash (%s): %v %v", conn, err, resp)
		}
		start := time.Now()
		mustCall(t, c, "GET", ts.URL+"/db?op=write&w=100&n=1&timeout=2&conn="+conn+q, nil, nil)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("a write after the crash (%s) waited %v for the lock", conn, d)
		}
	}
	mustCall(t, c, "GET", ts.URL+"/db?op=read"+q, nil, &r)
	if want := writers*rows + 2; r.Count != want || r.Counter != want {
		t.Errorf("after the crashed transactions: %d rows, counter %d; want %d", r.Count, r.Counter, want)
	}
	// The lock held for 2 s: a writer with a 100 ms busy timeout gives up after it, one
	// with 5 s waits for the holder. The writers start once the holder has the lock (a
	// fixed sleep raced a slow runner's holder), and 2 s leaves the 5 s writer its 200 ms
	// even when the first writer is slow to return.
	held := make(chan struct{})
	go func() {
		defer close(held)
		call(c, "GET", ts.URL+"/db?op=hold&ms=2000"+q, nil)
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var h struct{ Held bool }
		mustCall(t, c, "GET", ts.URL+"/db?op=held"+q, nil, &h)
		if h.Held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the holder did not take the write lock within 10 s")
		}
	}
	start := time.Now()
	resp, b, err := call(c, "GET", ts.URL+"/db?op=write&w=101&n=1&timeout=0.1"+q, nil)
	short := time.Since(start)
	if err != nil || resp.StatusCode != 503 || !strings.Contains(string(b), "database is locked") || short < 90*time.Millisecond {
		t.Errorf("a writer with a 100 ms busy timeout: %v %v %s after %v", err, resp, b, short)
	}
	start = time.Now()
	mustCall(t, c, "GET", ts.URL+"/db?op=write&w=102&n=1&timeout=5"+q, nil, nil)
	if long := time.Since(start); long < 200*time.Millisecond {
		t.Errorf("a writer with a 5 s busy timeout got the held lock after %v", long)
	}
	<-held
	mustCall(t, c, "GET", ts.URL+"/db?op=read"+q, nil, &r)
	if want := writers*rows + 3; r.Count != want || r.Counter != want {
		t.Errorf("after the busy writers: %d rows, counter %d; want %d", r.Count, r.Counter, want)
	}
}

// TestIOSQLiteThreadLocal: connections kept per thread (threading.local,
// check_same_thread=True) work, because a request runs on one OS thread from start to
// end, also across the detached writes of a streamed response, and a thread's state (and
// so its threading.local) belongs to that OS thread. 32 concurrent requests each query
// their thread's connection before each of 8 chunks of 64 KiB.
func TestIOSQLiteThreadLocal(t *testing.T) {
	ts, c := ioServer(t)
	dbPath := filepath.Join(dbDir(t), "tl.db")
	q := "&db=" + url.QueryEscape(dbPath)
	mustCall(t, c, "GET", ts.URL+"/db?op=init"+q, nil, nil)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			resp, b, err := call(c, "GET", ts.URL+"/db?op=stream&n=8"+q, nil)
			if err != nil || resp.StatusCode != 200 {
				t.Errorf("stream %d: %v %v", i, err, resp)
				return
			}
			var r struct {
				Threads int
				Errors  []string
			}
			if err := json.Unmarshal(b[bytes.LastIndexByte(b, '\n')+1:], &r); err != nil {
				t.Errorf("stream %d: %v", i, err)
			} else if r.Threads != 1 || len(r.Errors) > 0 || bytes.LastIndexByte(b, '\n')+1 != 8<<16 {
				first := ""
				if len(r.Errors) > 0 {
					first = r.Errors[0]
				}
				t.Errorf("stream %d: %d threads, %d errors (%.120s), %d bytes", i, r.Threads, len(r.Errors), first, len(b))
			}
		})
	}
	wg.Wait()
}

// TestIOFiles reads an 8 MiB file into a streamed response (iterated by the server, and
// through wsgi.file_wrapper) and writes a 5 MiB streamed request body into a file.
func TestIOFiles(t *testing.T) {
	ts, c := ioServer(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	data := payload(8<<20+7, 11)
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"gen", "wrapper"} {
		b := mustCall(t, c, "GET", ts.URL+"/file?chunk=1000000&mode="+mode+"&path="+url.QueryEscape(src), nil, nil)
		if !bytes.Equal(b, data) {
			t.Errorf("%s read: %d bytes, sha256 %s; want %d, %s", mode, len(b), sha(b), len(data), sha(data))
		}
	}
	upload := payload(5<<20+3, 12)
	dst := filepath.Join(dir, "dst.bin")
	var r struct {
		Sha256 string
		Len    int
	}
	mustCall(t, c, "POST", ts.URL+"/upload?path="+url.QueryEscape(dst), upload, &r)
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, upload) || r.Sha256 != sha(upload) || r.Len != len(upload) {
		t.Errorf("write: %v, file %d bytes, the app hashed %d bytes as %s; want %d, %s",
			err, len(got), r.Len, r.Sha256, len(upload), sha(upload))
	}
}

// TestIODNS resolves localhost with socket.getaddrinfo (libc) and connects to the
// upstream by name with http.client.
func TestIODNS(t *testing.T) {
	u := newUpstream(t)
	ts, c := ioServer(t)
	var r struct {
		Addrs  []string
		Status int
		Error  string
		Sha256 string
	}
	mustCall(t, c, "GET", ts.URL+"/dns?up="+url.QueryEscape(u.host), nil, &r)
	// http.client tries each address localhost resolves to, so an upstream on [::1]
	// (httptest found no free IPv4 loopback port) is reached as long as ::1 is one.
	if ip := u.Listener.Addr().(*net.TCPAddr).IP.String(); ip != "127.0.0.1" && !slices.Contains(r.Addrs, ip) {
		t.Skipf("the upstream fell back to %s, which localhost (%q) does not resolve to", ip, r.Addrs)
	}
	if !slices.Contains(r.Addrs, "127.0.0.1") || r.Status != 200 || r.Sha256 != sha(payload(100000, 9)) {
		t.Errorf("localhost: %q; GET via localhost: %d %s %s", r.Addrs, r.Status, r.Error, r.Sha256)
	}
	t.Logf("localhost: %q", r.Addrs)
}

// TestIOSubprocess runs a shell that copies 1 MiB of stdin to stdout with
// subprocess.run, one request per slot at once.
func TestIOSubprocess(t *testing.T) {
	ts, c := ioServer(t)
	in := payload(1<<20, 13)
	var wg sync.WaitGroup
	for i := range slots {
		wg.Go(func() {
			x := strconv.Itoa(i)
			resp, b, err := call(c, "POST", ts.URL+"/proc?x="+x, in)
			if want := append(slices.Clip(in), "done-"+x+"\n"...); err != nil || resp.StatusCode != 200 ||
				resp.Header.Get("X-Rc") != "0" || !bytes.Equal(b, want) {
				t.Errorf("subprocess %d: %v %v, %d bytes (want %d)", i, err, resp, len(b), len(want))
			}
		})
	}
	wg.Wait()
}

// dbDir is a temporary directory for a test database. The app keeps connections open
// after the test (per thread, in threading.local), and Windows cannot remove an open
// file, so there it is removed only as far as it can be.
func dbDir(t *testing.T) string {
	if runtime.GOOS != "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("", "pythago-wsgi-db-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
