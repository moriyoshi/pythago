// Package wsgi serves a WSGI application (PEP 3333) from Go: a Server is an
// http.Handler, so the application runs behind net/http (HTTP/1.1, HTTP/2, TLS,
// keep-alive and slow clients are Go's) and can share a mux with Go handlers.
//
// Each request runs app(environ, start_response) on the goroutine net/http serves it
// on, through [py.Do]: the goroutine's OS thread is the request's Python thread while it
// runs, so a WSGI server here is a thread pool whose size is the admission slots
// ([py.Config.Slots]: min(4, GOMAXPROCS) with the GIL by default, GOMAXPROCS
// free-threaded). Raise Slots for applications that block in Python (database drivers,
// time.sleep). Reading the request body and writing the response detach the thread state
// while they wait for the client, so slow clients hold neither the GIL nor a slot.
//
// Responses are streamed: each chunk the application yields (or passes to write()) is
// written and flushed before the next is asked for, as PEP 3333 requires, except when
// the application returns a list or tuple, whose length is known; that response gets a
// Content-Length unless the application set one, and is written after the application
// returns, from its bytes objects themselves (small chunks are copied and gathered), so the
// request does not need the GIL a second time to finish. The status and every header are
// validated before anything is written; a malformed one is an error in the application
// (a 500 when nothing was sent yet), never repaired. Hop-by-hop headers are refused, as
// PEP 3333 says.
//
// wsgi.file_wrapper sends a regular file from Go (with sendfile where the platform has
// it), from the file's current position; other file-likes are read in blocks.
//
//	py.Init(py.Config{Environment: venv})
//	srv, err := wsgi.New("myapp:app", wsgi.Options{})
//	hs := &http.Server{Addr: ":8000", Handler: srv, ReadHeaderTimeout: 10 * time.Second}
//	hs.ListenAndServe()
//
// Set http.Server timeouts: without ReadHeaderTimeout a client can hold a connection
// open forever by sending its headers slowly. Limits on request bodies (http.MaxBytesHandler)
// are the program's too.
//
// For more than one process (the usual way to scale a GIL build), see package workers.
package wsgi

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/moriyoshi/pythago/internal/arena"
	"github.com/moriyoshi/pythago/internal/httpfield"
	"github.com/moriyoshi/pythago/py"
)

//go:embed pythago_wsgi.py
var source string

// Options configure a Server.
type Options struct {
	// ScriptName is the path prefix the application is mounted under (SCRIPT_NAME). The
	// prefix is removed from PATH_INFO; a request outside it gets a 404.
	ScriptName string
	// MultiProcess sets wsgi.multiprocess: whether other processes serve the same
	// application (package workers).
	MultiProcess bool
	// Prefetch is the largest request body read before the application runs, when its
	// length is known and the client does not wait for 100 Continue (default 64 KiB);
	// wsgi.input then reads it from memory. Larger bodies are streamed.
	Prefetch int
}

// Server serves a WSGI application. It is an http.Handler.
type Server struct {
	opts   Options
	closed atomic.Bool

	mu    sync.RWMutex // read-held by requests in Python; Close takes it to release the objects
	serve *py.Object   // pythago_wsgi.serve
	app   *py.Object
	base  *py.Object // the environ's fixed entries

	// Bytes objects of bodies written after Python returned. Closing a reference needs a
	// thread state, which the writer no longer has: the next request in Python closes
	// them (closeWritten), or Close does.
	writtenMu   sync.Mutex
	written     []*py.Object
	writtenN    atomic.Int32
	writtenShut bool // Close closed them; later ones are left to py's cleanup
}

// Python's view of a request is its handle: its scope's ID, which no other lifetime gets.
// A stale handle (a write() kept past the response, on another thread) finds nothing,
// or a scope that serves another request by now, which acquire tells apart.
var reqs sync.Map // int64 -> *arena.Scope[request]

// requests are the scopes of requests, with their request as root. A request's scope
// is held by ServeHTTP until it returns and by every _pythago_wsgi call made for it
// (acquire); the last release recycles the request.
var requests = arena.NewPool(func(q *request) {
	q.mem.env.Reset()
	q.mem.pending.Reset()
	q.mem.pairs.Reset()
	*q = request{sc: q.sc, mem: q.mem}
})

// requestMem is what a request borrows for its lifetime, from lanes kept across
// recycling: the packed environ (built by ServeHTTP alone), the first buffer of small
// response chunks (under the request's lock) and the response's header pairs.
type requestMem struct {
	env     arena.Lane[byte]
	pending arena.Lane[byte]
	pairs   arena.SharedLane[[2]string]
	buf     py.Buffer // wsgi.input's reads
}

func newRequestMem() *requestMem {
	m := new(requestMem)
	m.env.Init(nil, true) // appended to: never read past what was written
	m.pending.Init(nil, true)
	return m
}

// acquire returns the request of handle a with a reference to its scope (its caller
// releases it), or nil if that request is over. A request found in reqs may end, and its
// scope serve another request, between the lookup and the reference: TryRetain fails for
// the other lifetime.
func acquire(a *py.Object, fn string) *request {
	h, err := a.Int64()
	if err != nil {
		return nil
	}
	v, ok := reqs.Load(h)
	if !ok {
		return nil
	}
	sc := v.(*arena.Scope[request])
	if !sc.TryRetain(h) {
		return nil
	}
	if onAcquire != nil {
		onAcquire(&sc.Root, h, fn)
	}
	return &sc.Root
}

// onAcquire, when set (tests only, export_test.go; before requests are served), runs in
// every _pythago_wsgi call (fn) that found its request h, once it holds the reference.
var onAcquire func(q *request, h int64, fn string)

type request struct {
	sc  *arena.Scope[request]
	mem *requestMem

	mu       sync.Mutex
	w        http.ResponseWriter
	r        *http.Request
	head     bool  // HEAD: the body is not sent
	started  bool  // the head was written
	flush    bool  // flush each chunk (the body's length was unknown)
	noBody   bool  // the status allows no body (1xx, 204, 304)
	declared int64 // the Content-Length the response promised, or -1
	done     bool
	body     int64     // body bytes the application handed over so far
	held     []segment // a known-length body's chunks before pending, written after Python returns
	pending  []byte    // small unflushed chunks, copied: written detached or after Python returns
	copied   int       // bytes copied into held and pending since the last detached write
	lastObj  uintptr   // the bytes object of the last held reference
	// pendingTaken: the first pending buffer was taken from mem (later ones are not,
	// so that a long body's copies are not all kept until the request ends).
	pendingTaken bool
}

// segment is a part of a known-length body that waits for Python to return: bytes copied
// in Go, or the storage of a bytes object that obj keeps alive (and unchanged: bytes are
// immutable). obj is nil for a copy, and for the same object repeated (an earlier
// segment holds it).
type segment struct {
	b   []byte
	obj *py.Object
}

// maxPending is how much small, unflushed body is copied and gathered in Go before it is
// written. Nothing is written to the client with the thread state attached: a client that
// stops reading (or an HTTP/2 flow-control window of 0) would block the write, and with it
// every Python thread.
const maxPending = 16 << 10

// minHeld is the smallest chunk of a known-length body held by reference, when copying it
// would pass maxPending; a smaller one is written detached with what waits before it, as
// for a streamed body (a reference costs more than copying a small chunk).
const minHeld = 4 << 10

// writeDetached runs a body write with the thread state detached: py.Release, which a
// test replaces to count them.
var writeDetached = py.Release

var (
	moduleOnce sync.Once
	moduleErr  error
	serveFn    *py.Object
)

// load creates _pythago_wsgi (the Go functions) and pythago_wsgi (the Python side, from
// source) once. Attached.
func load() error {
	moduleOnce.Do(func() {
		var g *py.Object
		if g, moduleErr = py.NewModule("_pythago_wsgi", module()); moduleErr != nil {
			return
		}
		g.Close()
		builtins, err := py.Import("builtins")
		if err != nil {
			moduleErr = err
			return
		}
		defer builtins.Close()
		ns, err := py.From(map[string]any{"_src": source, "__builtins__": builtins})
		if err != nil {
			moduleErr = err
			return
		}
		defer ns.Close()
		// A real module, importable (pythago_wsgi.ClientDisconnected), whose source
		// tracebacks can show.
		moduleErr = py.ExecIn(`
import linecache, sys, types
_m = types.ModuleType("pythago_wsgi")
_m.__file__ = "<pythago_wsgi>"
linecache.cache[_m.__file__] = (len(_src), None, _src.splitlines(True), _m.__file__)
exec(compile(_src, _m.__file__, "exec"), _m.__dict__)
sys.modules["pythago_wsgi"] = _m
`, ns)
		if moduleErr != nil {
			return
		}
		m, err := py.Import("pythago_wsgi")
		if err != nil {
			moduleErr = err
			return
		}
		defer m.Close()
		serveFn, moduleErr = m.Attr("serve")
	})
	return moduleErr
}

// New imports the application ("module:attribute", as gunicorn takes it). Call it after
// py.Init.
func New(app string, opts Options) (*Server, error) {
	mod, attr, ok := strings.Cut(app, ":")
	if !ok || mod == "" || attr == "" {
		return nil, fmt.Errorf("wsgi: application %q is not module:attribute", app)
	}
	if opts.ScriptName != "" && (!strings.HasPrefix(opts.ScriptName, "/") || strings.HasSuffix(opts.ScriptName, "/")) {
		return nil, fmt.Errorf("wsgi: ScriptName %q must start with / and not end with one", opts.ScriptName)
	}
	if opts.Prefetch <= 0 {
		opts.Prefetch = 64 << 10
	}
	s := &Server{opts: opts}
	err := py.Do(func() error {
		if err := load(); err != nil {
			return err
		}
		m, err := py.Import(mod)
		if err != nil {
			return err
		}
		defer m.Close()
		a := m
		for _, name := range strings.Split(attr, ".") {
			next, err := a.Attr(name)
			if a != m {
				a.Close()
			}
			if err != nil {
				return err
			}
			a = next
		}
		s.app = a
		g, err := py.Import("pythago_wsgi")
		if err != nil {
			return err
		}
		defer g.Close()
		fw, err := g.Attr("FileWrapper")
		if err != nil {
			return err
		}
		defer fw.Close()
		sys, err := py.Import("sys")
		if err != nil {
			return err
		}
		defer sys.Close()
		stderr, err := sys.Attr("stderr")
		if err != nil {
			return err
		}
		defer stderr.Close()
		s.base, err = py.From(map[string]any{
			"wsgi.version":          py.Tuple{1, 0},
			"wsgi.errors":           stderr,
			"wsgi.multithread":      true,
			"wsgi.multiprocess":     opts.MultiProcess,
			"wsgi.run_once":         false,
			"wsgi.input_terminated": true,
			"wsgi.file_wrapper":     fw,
			"SCRIPT_NAME":           opts.ScriptName,
		})
		if err != nil {
			return err
		}
		s.serve = serveFn.NewRef()
		return nil
	})
	if err != nil {
		if s.app != nil {
			py.Do(func() error { s.app.Close(); return nil })
		}
		return nil, fmt.Errorf("wsgi: %w", err)
	}
	return s, nil
}

// Close stops accepting requests (they get 503) and releases the application, after the
// requests in Python finish. Call it before py.Finalize.
func (s *Server) Close() error {
	s.closed.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.serve == nil {
		return nil
	}
	err := py.Do(func() error {
		s.serve.Close()
		s.app.Close()
		s.base.Close()
		s.writtenMu.Lock()
		s.writtenShut = true
		s.writtenMu.Unlock()
		s.closeWritten()
		return nil
	})
	s.serve, s.app, s.base = nil, nil, nil
	return err
}

// releaseWritten hands the references of a body written after Python returned to
// closeWritten. After Close, they are dropped instead, and py's cleanup releases them.
func (s *Server) releaseWritten(held []segment) {
	s.writtenMu.Lock()
	defer s.writtenMu.Unlock()
	if s.writtenShut {
		return
	}
	for _, sg := range held {
		if sg.obj != nil {
			s.written = append(s.written, sg.obj)
		}
	}
	s.writtenN.Store(int32(len(s.written)))
}

// closeWritten closes the references releaseWritten queued. Attached.
func (s *Server) closeWritten() {
	if s.writtenN.Load() == 0 {
		return
	}
	s.writtenMu.Lock()
	objs := s.written
	s.written = nil
	s.writtenN.Store(0)
	s.writtenMu.Unlock()
	for _, o := range objs {
		o.Close()
	}
}

// serve's results (pythago_wsgi.DONE, FAILED, ABORT).
const (
	resultDone   = 0
	resultFailed = 1
	resultAbort  = 2
)

// ServeHTTP runs the application for the request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.closed.Load() {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodConnect {
		http.Error(w, "CONNECT is not supported", http.StatusMethodNotAllowed)
		return
	}
	pathInfo, ok := s.pathInfo(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sc := requests.Get()
	defer sc.Release() // the last of ServeHTTP's defers: q is not used after it
	q := &sc.Root
	if q.sc == nil { // a new one
		q.sc, q.mem = sc, newRequestMem()
	}
	packed, ok := environ(q.mem.env.Rest(), r, pathInfo)
	q.mem.env.Adopt(packed)
	if !ok {
		http.Error(w, "invalid header value", http.StatusBadRequest)
		return
	}
	// Small bodies of known length are read before the application runs, which then
	// reads them without leaving Python. A client that waits for 100 Continue gets it
	// only when the application reads.
	var body any // nil (None): streamed
	switch n := r.ContentLength; {
	case n == 0 || r.Body == nil || r.Body == http.NoBody:
		body = []byte{}
	case n > 0 && n <= int64(s.opts.Prefetch) && r.Header.Get("Expect") == "":
		b, err := prefetch(r.Body, n)
		if err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		body = b
	}

	q.w, q.r, q.head, q.declared = w, r, r.Method == http.MethodHead, -1
	h := sc.ID()
	reqs.Store(h, sc)
	defer reqs.Delete(h)

	result := resultFailed
	s.mu.RLock()
	err := ErrClosed
	if s.serve != nil {
		err = py.Do(func() error {
			s.closeWritten()
			res, err := s.serve.Call(s.app, s.base, h, packed, body)
			if err != nil {
				return err
			}
			defer res.Close()
			v, err := res.Int64()
			result = int(v)
			return err
		})
	}
	s.mu.RUnlock()
	if err != nil {
		// serve handles the application's errors; this is the call itself.
		log.Printf("wsgi: %v", err)
		result = resultFailed
	}
	// Over: from here on, Python (another thread holding a write()) finds nothing.
	q.mu.Lock()
	q.done = true
	started, held, pending := q.started, q.held, q.pending
	q.held, q.pending = nil, nil
	q.mu.Unlock()
	if len(held) > 0 {
		defer s.releaseWritten(held)
	}
	switch {
	case result == resultFailed && !started:
		for k := range w.Header() {
			delete(w.Header(), k)
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	case result != resultDone:
		// The head is out: the client must not see a complete response. A body
		// shorter than its Content-Length needs nothing here: net/http closes the
		// connection (HTTP/1.1) or resets the stream (HTTP/2) itself.
		panic(http.ErrAbortHandler)
	case len(held) > 0 || len(pending) > 0:
		// The rest of a known-length body, in order; a client gone stops it.
		var err error
		for _, sg := range held {
			if _, err = w.Write(sg.b); err != nil {
				break
			}
		}
		if err == nil && len(pending) > 0 {
			_, err = w.Write(pending)
		}
		if errors.Is(err, http.ErrContentLength) {
			log.Printf("wsgi: %s %s: the response body is longer than its Content-Length", r.Method, r.URL.Path)
			panic(http.ErrAbortHandler)
		}
	}
}

// prefetch reads a body of declared length n, growing the buffer as data arrives: memory
// follows what the client sent, not what it declared (a 64 KiB Content-Length and no
// body would otherwise cost 64 KiB per connection).
func prefetch(body io.Reader, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(body, n))
	if err == nil && int64(len(b)) != n {
		err = io.ErrUnexpectedEOF
	}
	return b, err
}

// ErrClosed is the error of a request that arrives as the Server closes.
var ErrClosed = errors.New("wsgi: server closed")

// pathInfo removes the ScriptName prefix from path.
func (s *Server) pathInfo(path string) (string, bool) {
	sn := s.opts.ScriptName
	if sn == "" {
		return path, true
	}
	if path == sn {
		return "", true
	}
	if rest, ok := strings.CutPrefix(path, sn); ok && strings.HasPrefix(rest, "/") {
		return rest, true
	}
	return "", false
}

// hopByHop are the headers PEP 3333 forbids applications to send (connection management
// is the server's), and Proxy-Connection.
var hopByHop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true,
	"te": true, "trailer": true, "trailers": true, "transfer-encoding": true, "upgrade": true,
	"proxy-connection": true,
}

// parseStatus takes a WSGI status line, "200 OK": three digits, a space and a reason
// phrase. net/http writes its own reason phrase.
func parseStatus(status []byte) (int, error) {
	if len(status) < 4 || status[3] != ' ' {
		return 0, fmt.Errorf("invalid WSGI status %q (want \"200 OK\")", status)
	}
	code := 0
	for _, c := range status[:3] {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid WSGI status %q (want \"200 OK\")", status)
		}
		code = code*10 + int(c-'0')
	}
	if code < 200 || code > 599 {
		// 1xx would be an interim response (101 would switch protocols); others make
		// net/http panic.
		return 0, fmt.Errorf("invalid WSGI status %q: the code must be 200-599", status)
	}
	if !httpfield.ValidValue(string(status[4:])) {
		return 0, fmt.Errorf("invalid WSGI status %q (control characters)", status)
	}
	return code, nil
}

// parseHeaders splits the packed "name\0value\0..." headers and checks them against
// count, the number of pairs Python packed: a NUL inside a name or value would shift the
// pairs (and could inject a header), and changes the field count. The pairs are taken
// from lane (a request's, if not nil); the strings share one copy of packed.
func parseHeaders(lane *arena.SharedLane[[2]string], packed []byte, count int64) ([][2]string, int64, error) {
	declared := int64(-1)
	if count == 0 {
		if len(packed) != 0 {
			return nil, 0, errors.New("WSGI headers do not match their count")
		}
		return nil, declared, nil
	}
	if count < 0 || count > 1<<16 {
		return nil, 0, errors.New("invalid number of WSGI headers")
	}
	// 2*count fields: as many NULs, but for the last field's.
	if int64(bytes.Count(packed, []byte{0})) != 2*count-1 {
		return nil, 0, errors.New("WSGI headers must be (name, value) pairs of str without NUL")
	}
	var pairs [][2]string
	if lane != nil {
		pairs = lane.Take(int(count))
	} else {
		pairs = make([][2]string, count)
	}
	rest := string(packed)
	for i := range pairs {
		name, rest0, _ := strings.Cut(rest, "\x00")
		value, rest1, _ := strings.Cut(rest0, "\x00")
		rest = rest1
		if !httpfield.ValidName(name) {
			return nil, 0, fmt.Errorf("invalid WSGI header name %q", name)
		}
		if !httpfield.ValidValue(value) {
			return nil, 0, fmt.Errorf("invalid WSGI header value for %q (control characters)", name)
		}
		var lowerBuf [32]byte
		lower := lowerName(lowerBuf[:0], name)
		if hopByHop[string(lower)] {
			return nil, 0, fmt.Errorf("WSGI applications must not send the hop-by-hop header %q", name)
		}
		if string(lower) == "content-length" {
			if declared >= 0 {
				return nil, 0, errors.New("more than one content-length")
			}
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 || strings.TrimLeft(value, "0123456789") != "" {
				return nil, 0, fmt.Errorf("invalid content-length %q", value)
			}
			declared = n
		}
		pairs[i] = [2]string{name, value}
	}
	return pairs, declared, nil
}

// lowerName appends name in lower case to dst (a header name is a token: ASCII), for a
// map lookup or a comparison that copies nothing.
func lowerName(dst []byte, name string) []byte {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst = append(dst, c)
	}
	return dst
}

// head is a validated response head, not yet written.
type head struct {
	code     int
	pairs    [][2]string
	declared int64 // the application's Content-Length, or -1
}

// parseHead validates the status and the headers Python packed. Nothing is written, so
// an error still leaves the request a clean 500.
func parseHead(q *request, status, packed, count *py.Object) (head, error) {
	st, err := status.UnsafeBytes()
	if err != nil {
		return head{}, err
	}
	code, err := parseStatus(st)
	if err != nil {
		return head{}, err
	}
	b, err := packed.UnsafeBytes()
	if err != nil {
		return head{}, err
	}
	n, err := count.Int64()
	if err != nil {
		return head{}, err
	}
	pairs, declared, err := parseHeaders(&q.mem.pairs, b, n)
	if err != nil {
		return head{}, err
	}
	return head{code, pairs, declared}, nil
}

// bodyless reports whether the status allows no body.
func (hd head) bodyless() bool {
	return hd.code == http.StatusNoContent || hd.code == http.StatusNotModified
}

// start writes the head and, when the body's length is known (length >= 0), a
// Content-Length unless the application set one. Locked.
func (q *request) start(hd head, length int64) {
	q.noBody = hd.bodyless()
	hdr := q.w.Header()
	for _, kv := range hd.pairs {
		k := http.CanonicalHeaderKey(kv[0])
		hdr[k] = append(hdr[k], kv[1])
	}
	declared := hd.declared
	if declared < 0 && length >= 0 && !q.noBody {
		hdr["Content-Length"] = []string{strconv.FormatInt(length, 10)}
		declared = length
	}
	if q.noBody {
		declared = -1 // net/http sends none, whatever the header says
	}
	q.declared, q.flush = declared, length < 0
	q.started = true
	q.w.WriteHeader(hd.code)
}

var errTooLong = errors.New("the response body is longer than its Content-Length")

// write takes a body chunk from the bytes object data. A streamed body's chunk is written
// and flushed at once, from the object (no copy), with the thread state detached while the
// client is slow. A known-length body (a list or tuple) waits for Python to return:
// small chunks are copied into pending, larger ones held by reference; only a small chunk
// past maxPending of copies is written now, with what waits before it. It reports whether
// the client is gone. Locked.
func (q *request) write(data *py.Object) (bool, error) {
	b, err := data.UnsafeBytes()
	if err != nil {
		return false, err
	}
	if q.noBody && len(b) > 0 {
		return false, errors.New("this response status has no body")
	}
	if q.head || len(b) == 0 && !q.flush {
		return false, nil
	}
	if !q.flush {
		if q.copied+len(b) <= maxPending {
			if q.pending == nil && !q.pendingTaken {
				// The pending copies before the next detached write fit in maxPending.
				q.pending, q.pendingTaken = q.mem.pending.Take(maxPending)[:0], true
			}
			q.pending = append(q.pending, b...) // written after Python returns, or with the next small chunk past maxPending
			q.copied += len(b)
			q.body += int64(len(b))
			return false, nil
		}
		if len(b) >= minHeld {
			// As net/http would refuse it when written: the application learns it here.
			if q.declared >= 0 && q.body+int64(len(b)) > q.declared {
				return false, errTooLong
			}
			if len(q.pending) > 0 {
				q.held = append(q.held, segment{b: q.pending})
				q.pending = nil
			}
			sg := segment{b: b}
			if p := data.Ptr(); p != q.lastObj {
				sg.obj, q.lastObj = data.NewRef(), p
			}
			q.held = append(q.held, sg)
			q.body += int64(len(b))
			return false, nil
		}
	}
	held, pending := q.held, q.pending
	q.held, q.pending, q.copied, q.lastObj = nil, nil, 0, 0
	q.body += int64(len(b))
	writeDetached(func() {
		for _, sg := range held {
			if _, err = q.w.Write(sg.b); err != nil {
				return
			}
		}
		if len(pending) > 0 {
			if _, err = q.w.Write(pending); err != nil {
				return
			}
		}
		if len(b) > 0 {
			if _, err = q.w.Write(b); err != nil {
				return
			}
		}
		if q.flush {
			err = http.NewResponseController(q.w).Flush()
		}
	})
	for _, sg := range held {
		sg.obj.Close() // attached again
	}
	if errors.Is(err, http.ErrContentLength) {
		return false, errTooLong
	}
	return err != nil, nil
}

// sendFile writes the head and the regular file fd from offset: Content-Length bytes if
// the application set it, else to the end. It reports 2 when fd is not a regular file.
// fd is a descriptor, or on Windows a file handle (openRegular).
func (q *request) sendFile(status, packed, count *py.Object, fd, offset int64) (int, error) {
	fileSize, ok := regularSize(fd)
	if !ok {
		return 2, nil
	}
	remain := fileSize - offset
	if offset < 0 || remain < 0 {
		return 2, nil
	}
	hd, err := parseHead(q, status, packed, count)
	if err != nil {
		return 0, err
	}
	if hd.declared > remain {
		return 0, fmt.Errorf("file_wrapper: Content-Length %d is longer than the %d bytes left in the file", hd.declared, remain)
	}
	q.start(hd, remain)
	size := q.declared
	if q.head || q.noBody || size <= 0 {
		return 0, nil
	}
	// The file is sent from its current offset, which a duplicate shares with Python's
	// file object: as with read(), one file object must not serve two responses at once.
	// A duplicate: Python owns fd, and closes it when the response is done.
	f, err := openDuplicate(fd)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var gone bool
	py.Release(func() {
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			return
		}
		_, err = io.CopyN(q.w, f, size)
		gone = err != nil
	})
	if errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("file_wrapper: the file shrank while being sent: %w", io.ErrUnexpectedEOF)
	}
	if gone {
		return 1, nil
	}
	return 0, err
}

// readInto reads request body into the writable buffer b, detached. It returns the
// number of bytes (0 at the end), or -1 when the body cannot be read.
func (q *request) readInto(b *py.Object) (int, error) {
	if err := b.BufferInto(&q.mem.buf, true); err != nil {
		return 0, err
	}
	defer q.mem.buf.Close()
	p, err := q.mem.buf.Bytes()
	if err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	var n int
	py.Release(func() {
		for n == 0 && err == nil {
			n, err = q.r.Body.Read(p)
		}
	})
	switch {
	case n > 0:
		return n, nil
	case errors.Is(err, io.EOF):
		return 0, nil
	}
	return -1, nil
}

// ---- the _pythago_wsgi module ----

func module() map[string]any {
	// with runs fn on the request h, unless it is over; Python learns that as a
	// disconnect.
	with := func(name string, a *py.Object, gone any, fn func(q *request) (any, error)) (any, error) {
		q := acquire(a, name)
		if q == nil {
			return gone, nil
		}
		defer q.sc.Release() // after the unlock below: q is not used once it is released
		if !q.mu.TryLock() {
			// Another thread of this request holds it, possibly detached in a slow read
			// or write and about to need its thread state back: wait detached, or the
			// two would wait for each other (and, with the GIL, stop every thread).
			py.Release(q.mu.Lock)
		}
		defer q.mu.Unlock()
		if q.done {
			return gone, nil
		}
		return fn(q)
	}
	return map[string]any{
		// respond(h, status, headers, count, data, length) -> whether the client is gone:
		// the head and the first body chunk. length is the body's, or -1 if unknown.
		"respond": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			return with("respond", a[0], true, func(q *request) (any, error) {
				if q.started {
					return nil, errors.New("the response has started already")
				}
				length, err := a[5].Int64()
				if err != nil {
					return nil, err
				}
				hd, err := parseHead(q, a[1], a[2], a[3])
				if err != nil {
					return nil, err
				}
				if b, err := a[4].UnsafeBytes(); err != nil {
					return nil, err
				} else if hd.bodyless() && len(b) > 0 {
					return nil, fmt.Errorf("a %d response has no body", hd.code)
				}
				q.start(hd, length)
				return q.write(a[4])
			})
		}),
		// body(h, data) -> whether the client is gone
		"body": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			return with("body", a[0], true, func(q *request) (any, error) {
				if !q.started {
					return nil, errors.New("the response has not started")
				}
				return q.write(a[1])
			})
		}),
		// sendfile(h, status, headers, count, fd, offset) -> 0 sent, 1 the client is gone,
		// 2 not a regular file (nothing was sent)
		"sendfile": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			return with("sendfile", a[0], 1, func(q *request) (any, error) {
				if q.started {
					return 2, nil
				}
				fd, err := a[4].Int64()
				if err != nil {
					return nil, err
				}
				offset, err := a[5].Int64()
				if err != nil {
					return nil, err
				}
				return q.sendFile(a[1], a[2], a[3], fd, offset)
			})
		}),
		// readinto(h, buffer) -> bytes read (0 at the end), or -1 if the body cannot be read
		"readinto": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			return with("readinto", a[0], -1, func(q *request) (any, error) {
				return q.readInto(a[1])
			})
		}),
	}
}
