// Package asgi serves an ASGI application from Go: a Server is an http.Handler, so the
// application runs behind net/http (HTTP/1.1, HTTP/2, TLS, keep-alive and slow clients
// are Go's) and can share a mux with Go handlers.
//
// Each worker is a Python thread running a goaio.GoEventLoop, with its own ASGI lifespan
// (as each uvicorn worker process has). A request's goroutine runs app(scope, receive,
// send) itself, as an eager task of a worker's loop, together with the requests that
// arrive meanwhile; a request that awaits continues on the loop's thread (with
// Options.LoopThread, every request is posted there). send() queues response actions that
// the goroutine performs (the body is written from the bytes objects, without copying),
// and receive() asks the goroutine for more of the request body.
//
//	cfg := py.Config{Environment: venv}
//	asgi.Configure(&cfg)
//	py.Init(cfg)
//	srv, err := asgi.New("myapp:app", asgi.Options{})
//	hs := &http.Server{Addr: ":8000", Handler: srv, ReadHeaderTimeout: 10 * time.Second}
//	hs.ListenAndServe()
//
// Set http.Server timeouts: without ReadHeaderTimeout a client can hold a connection
// open forever by sending its headers slowly.
package asgi

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/moriyoshi/pythago/aio"
	"github.com/moriyoshi/pythago/internal/arena"
	"github.com/moriyoshi/pythago/internal/fusion"
	"github.com/moriyoshi/pythago/py"
)

//go:embed pythago_asgi.py
var source string

// Configure makes the ASGI support importable in the interpreter cfg configures (it also
// configures package aio). Call it before py.Init.
func Configure(cfg *py.Config) {
	aio.AddSource("pythago_asgi", source)
	aio.Configure(cfg)
	if cfg.Modules == nil {
		cfg.Modules = map[string]map[string]any{}
	}
	cfg.Modules["_pythago_asgi"] = module()
}

// Options configure a Server.
type Options struct {
	// Workers is the number of event loops (Python threads). The default is 1 with the
	// GIL, where more loops only contend for it, and GOMAXPROCS on free-threaded builds.
	Workers int
	// RootPath is the ASGI root_path: the path prefix the application is mounted under.
	RootPath string
	// Lifespan is "auto" (the default: run the lifespan protocol if the application
	// supports it), "on" (it must) or "off".
	Lifespan string
	// BodyChunk is the most request body passed per http.request message (default 64 KiB).
	BodyChunk int
	// HighWater is how much response body may be queued for a request before send()
	// waits for the client (default 64 KiB).
	HighWater int
	// WebSocketMaxMessage limits incoming WebSocket messages (default 16 MiB, as
	// uvicorn); a larger one closes the connection (1009).
	WebSocketMaxMessage int64
	// WebSocketOrigins are host patterns (path.Match; "scheme://host" when the pattern
	// has "://") of browser origins allowed besides the request's own host.
	WebSocketOrigins []string
	// WebSocketAnyOrigin skips the origin check, leaving it to the application.
	// Without one, any web page can open a WebSocket with the user's cookies.
	WebSocketAnyOrigin bool
	// PathSendRoots, if set, confines http.response.pathsend to files under these
	// directories (os.Root: no ".." or symlink escapes). Otherwise any file the process
	// can read may be sent, as the application could read it itself.
	PathSendRoots []string
	// LoopThread hands every HTTP request to the worker's loop thread. By default
	// requests run inline instead: the goroutine serving a request runs the application
	// as an eager task on the worker's event loop, and so do the requests that arrive
	// while it does (flat combining), while the loop thread is kept out. A request that
	// awaits anything the event loop must deliver continues on the loop thread. Inline,
	// application code runs on whichever thread served it, so threading.local and thread
	// identity are per batch, not per request (contextvars are unaffected); LoopThread
	// gives each worker's requests one thread, at a cost (on one core, CPython 3.13:
	// 46.5k req/s against 58.5k inline). WebSockets always go through the loop thread.
	LoopThread bool
}

// Server serves an ASGI application. It is an http.Handler.
type Server struct {
	id      int64
	opts    Options
	workers []*worker
	next    atomic.Uint64
	attach  chan error
	detach  chan int
	closing atomic.Bool

	mu   sync.Mutex
	reqs map[int64]*request
	seq  int64

	roots []pathRoot // Options.PathSendRoots

	// onCall, when set (tests only: export_test.go, before the server serves), runs in
	// every _pythago_asgi call that found its request, once it holds the reference.
	onCall func(*request)
}

type worker struct {
	loop  *aio.Loop
	token int64

	// Inline mode (the default; not Options.LoopThread). Whoever holds the worker (busy)
	// runs its Python: the loop thread while it is not waiting for events, or a request
	// goroutine that combines. Requests queue in pending and run on the holder's thread, in batches.
	serve    *py.Object // _Worker.serve_inline
	mu       sync.Mutex
	busy     bool
	pending  []*request             // inline requests to run, each holding a reference
	loopWait chan struct{}          // the loop thread waits to hold the worker again
	done     []interface{ Close() } // released by goroutines; closed by the holder

	// The holder's own (no lock: one holder at a time).
	spare []*request                // pending's other array, swapped in by Serve
	args  []any                     // a batch's serve_inline arguments
	addrs map[addrKey][2]*py.Object // server addresses' host and port objects (serverAddr)
}

// release closes c (it needs a thread state) on the worker's Python thread: the loop
// thread at its next poll, or, inline, whoever holds the worker next. Inline requests
// may never wake the loop thread, so their objects must not wait for it.
func (w *worker) release(c interface{ Close() }) {
	if w.serve == nil {
		w.loop.Release(c)
		return
	}
	w.mu.Lock()
	w.done = append(w.done, c)
	w.mu.Unlock()
}

// submit queues an inline request (q.in) and, unless someone holds the worker, holds it
// and runs every queued request on this goroutine's thread. The queue holds a reference
// to q until serve_inline has taken its values; the critical section is a pointer append
// to an array the holders reuse.
func (w *worker) submit(q *request) {
	q.sc.Retain()
	w.mu.Lock()
	w.pending = append(w.pending, q)
	if w.busy {
		w.mu.Unlock() // the holder runs it
		return
	}
	w.busy = true
	w.mu.Unlock()
	py.Do(func() error {
		w.Unlock()
		return nil
	})
}

// Serve runs the queued requests in batches until none is left. The caller holds the
// worker and is attached.
func (w *worker) Serve() {
	for {
		w.mu.Lock()
		batch, done := w.pending, w.done
		w.pending, w.done, w.spare = w.spare, nil, nil
		w.mu.Unlock()
		for _, c := range done {
			c.Close()
		}
		if len(batch) == 0 {
			w.spare = batch
			return
		}
		args := w.args[:0]
		w.args = nil // in case this is ever reentered
		for _, q := range batch {
			args = w.appendInline(args, q)
		}
		// The values are converted (strings and bytes copied) before serve_inline runs:
		// nothing Python keeps refers to Go memory, and the requests may be recycled.
		r, err := w.serve.Call(args...)
		clear(args)
		w.args = args[:0]
		for i, q := range batch {
			if fb := q.in.fused; fb != nil { // a slot the call did not convert goes back
				fb.Release()
				q.in.fused = nil
			}
			q.unref()
			batch[i] = nil
		}
		w.spare = batch[:0]
		if err != nil {
			// serve_inline handles each request's errors; this is the call itself.
			log.Printf("asgi: inline requests failed: %v", err)
			continue
		}
		wake, _ := r.Truth()
		r.Close()
		if wake { // tasks continue on the loop thread, or it has callbacks to run
			w.loop.Wake()
		}
	}
}

// Unlock runs what is queued and lets go of the worker: to the loop thread if it waits
// for it, otherwise to whoever submits next. Attached.
func (w *worker) Unlock() {
	for {
		w.Serve()
		w.mu.Lock()
		if len(w.pending) > 0 { // queued after Serve looked: still ours to run
			w.mu.Unlock()
			continue
		}
		if w.loopWait != nil {
			close(w.loopWait) // busy passes to the loop thread
			w.loopWait = nil
		} else {
			w.busy = false
		}
		w.mu.Unlock()
		return
	}
}

// Lock is the loop thread taking the worker back after it waited for events (thread
// state released); it waits for a combining goroutine to finish.
func (w *worker) Lock() {
	w.mu.Lock()
	if !w.busy {
		w.busy = true
		w.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	w.loopWait = ch
	w.mu.Unlock()
	<-ch
}

// inlineArgs are the values of an inline request that ServeHTTP prepares and the holder
// passes to serve_inline (appendInline). The byte slices share request.buf.
type inlineArgs struct {
	method, version, scheme any // constants (*py.Object), or a string
	rawPath, query, headers []byte
	path                    string
	client                  bool // clientHost and clientPort are set
	clientHost              string
	clientPort              int
	local                   net.Addr // the server's end of the connection, or nil
	body                    []byte
	more                    bool
	fused                   *fusion.Bytes // body's slot, when the fusion took one (body aliases it)
}

// prepare sets q.in from r: hlen is headerSize(r), body and more its first body chunk.
func (q *request) prepare(r *http.Request, hlen int, body []byte, more bool) {
	version, scheme := protocol(r)
	rawPath := r.URL.EscapedPath()
	buf := q.mem.bytes.Take(len(rawPath) + len(r.URL.RawQuery) + hlen)[:0]
	buf = append(buf, rawPath...)
	buf = append(buf, r.URL.RawQuery...)
	buf = packHeaders(buf, r)
	n, m := len(rawPath), len(rawPath)+len(r.URL.RawQuery)
	in := &q.in
	in.method, in.version, in.scheme = constant(r.Method), constant(version), constant(scheme)
	in.rawPath, in.query, in.headers = buf[:n:n], buf[n:m:m], buf[m:]
	in.path = validPath(r.URL.Path)
	in.clientHost, in.clientPort, in.client = splitAddr(r.RemoteAddr)
	in.local, _ = r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	in.body, in.more = body, more
}

// appendInline appends q as serve_inline takes a request: h, method, http_version,
// scheme, raw_path, path, query_string, packed headers, client host and port, server host
// and port, body, more_body, trailers offered. Common strings, empty bytes and the
// server's address are Python objects made once, which saves creating them per request
// (and on free-threaded builds, the strings are immortal and the port is the worker's
// own: no reference counts shared between workers). The holder calls it, attached.
func (w *worker) appendInline(args []any, q *request) []any {
	in := &q.in
	var ch, cp any
	if in.client {
		ch, cp = in.clientHost, in.clientPort
	}
	sh, sp := w.serverAddr(in.local)
	return append(args, q.h, in.method, in.version, in.scheme, in.rawPath, in.path, bytesArg(in.query),
		in.headers, ch, cp, sh, sp, bodyArg(in), in.more, q.offered)
}

// bodyArg is the body's argument: its fused slot, which the conversion makes the bytes
// object in place, else the body (copied).
func bodyArg(in *inlineArgs) any {
	if in.fused != nil {
		return in.fused
	}
	return bytesArg(in.body)
}

// fusedStart is the first slot's room for a body chunk of unknown length.
const fusedStart = 4 << 10

// readFusedBody reads a chunk of up to limit bytes of a body with n bytes left (-1:
// unknown) into a fused slot (py.Config.FusedBytes), which becomes the bytes object
// without a copy. A known length up to limit takes a slot of that size and is read on to
// its EOF; a longer one takes limit. An unknown length starts at fusedStart: a full slot
// is probed for one more byte and, if the body goes on, the chunk moves to a slot four
// times as large (a copy), up to limit. ok is false if no slot was taken (the fusion is
// off, the chunk is above the largest class, or no memory is left): nothing was read. If
// a move finds no slot, the chunk continues on the heap: fb is nil and data the chunk.
// On an error, nothing is held.
func readFusedBody(body io.Reader, n int64, limit int) (fb *fusion.Bytes, data []byte, more, ok bool, err error) {
	known := n >= 0 && n <= int64(limit)
	room := limit
	switch {
	case known:
		room = int(n)
	case n < 0:
		room = min(limit, fusedStart)
	}
	if fb = fusion.New(room); fb == nil {
		return nil, nil, false, false, nil
	}
	buf := fb.Buf()
	got := 0
	for {
		for got < len(buf) {
			m, err := body.Read(buf[got:])
			got += m
			switch {
			case errors.Is(err, io.EOF):
				fb.Truncate(got)
				return fb, buf[:got], false, true, nil
			case err != nil:
				fb.Release()
				return nil, nil, false, true, err
			}
		}
		fb.Truncate(got)
		if known {
			// Read on to the body's EOF, as readFirstChunk does.
			var probe [1]byte
			if m, err := body.Read(probe[:]); m > 0 {
				fb.Release()
				return nil, nil, false, true, errors.New("asgi: the request body is longer than its Content-Length")
			} else if err != nil && !errors.Is(err, io.EOF) {
				fb.Release()
				return nil, nil, false, true, err
			}
			return fb, buf, false, true, nil
		}
		if got >= limit || n > int64(limit) {
			return fb, buf, true, true, nil // a full chunk of a longer or unknown body
		}
		// A full slot below the limit: does the body go on?
		var probe [1]byte
		m, perr := 0, error(nil)
		for m == 0 && perr == nil {
			m, perr = body.Read(probe[:])
		}
		if m == 0 {
			if errors.Is(perr, io.EOF) {
				return fb, buf, false, true, nil
			}
			fb.Release()
			return nil, nil, false, true, perr
		}
		nb := fb.Grow(min(len(buf)*4, limit))
		if nb == nil { // no slot for the move: go on on the heap
			data = append(make([]byte, 0, min(limit, 4*len(buf))), buf...)
			data = append(data, probe[0])
			fb.Release()
			if errors.Is(perr, io.EOF) {
				return nil, data, false, true, nil
			}
			data, more, err = readChunkInto(data, body, limit)
			return nil, data, more, true, err
		}
		fb, buf = nb, nb.Buf()
		buf[got] = probe[0]
		got++
		if errors.Is(perr, io.EOF) {
			fb.Truncate(got)
			return fb, buf[:got], false, true, nil
		}
	}
}

// bytesArg is b as an argument: the shared empty bytes object when b is empty.
func bytesArg(b []byte) any {
	if len(b) == 0 {
		return constants.empty
	}
	return b
}

// constants are Python objects for strings most requests carry (methods, HTTP versions,
// schemes) and empty bytes, made once (newConstants) and never released. The strings are
// interned: free-threaded builds make interned strings immortal, so the requests of all
// workers can share them without contending for their reference counts.
var constants struct {
	mu     sync.Mutex // guards making them; they are read once a server is up
	strs   map[string]*py.Object
	empty  *py.Object
	intern *py.Object // sys.intern
}

func newConstants() error {
	constants.mu.Lock()
	defer constants.mu.Unlock()
	if constants.strs != nil {
		return nil
	}
	sys, err := py.Import("sys")
	if err != nil {
		return err
	}
	defer sys.Close()
	if constants.intern, err = sys.Attr("intern"); err != nil {
		return err
	}
	strs := map[string]*py.Object{}
	for _, v := range []string{"GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS", "PATCH",
		"1.0", "1.1", "2", "http", "https"} {
		o, err := internString(v)
		if err != nil {
			return err
		}
		strs[v] = o
	}
	empty, err := py.From([]byte{}) // the empty bytes singleton: immortal (3.12+)
	if err != nil {
		return err
	}
	constants.strs, constants.empty = strs, empty
	return nil
}

// internString returns sys.intern(s). Attached; newConstants made constants.intern.
func internString(s string) (*py.Object, error) {
	o, err := py.From(s)
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return constants.intern.Call(o)
}

// constant returns s's constant object, or s.
func constant(s string) any {
	if o := constants.strs[s]; o != nil {
		return o
	}
	return s
}

// addrKey identifies a TCP address without formatting it.
type addrKey struct {
	ip   [16]byte
	n    int // len(IP): a 4-byte and a 16-byte form get entries of their own
	port int
	zone string
}

// maxAddrs bounds a worker's server address cache: a server listens on a few addresses.
const maxAddrs = 64

// serverAddr returns the host and port of the server's address (the scope's "server") as
// objects the worker made once per address: the host interned, the port the worker's own
// (an int cannot be made immortal; one shared by all workers would have its reference
// count contended). The holder calls it, attached.
func (w *worker) serverAddr(a net.Addr) (host, port any) {
	if a == nil {
		return nil, nil
	}
	ta, ok := a.(*net.TCPAddr)
	var k addrKey
	if ok && len(ta.IP) <= len(k.ip) {
		copy(k.ip[:], ta.IP)
		k.n, k.port, k.zone = len(ta.IP), ta.Port, ta.Zone
		if hp, ok := w.addrs[k]; ok {
			return hp[0], hp[1]
		}
	} else {
		ok = false
	}
	h, p, valid := splitAddr(a.String())
	if !valid {
		return nil, nil
	}
	if !ok || len(w.addrs) >= maxAddrs {
		return h, p
	}
	ho, err := internString(h)
	if err != nil {
		return h, p
	}
	po, err := py.From(p)
	if err != nil {
		ho.Close()
		return h, p
	}
	if w.addrs == nil {
		w.addrs = map[addrKey][2]*py.Object{}
	}
	w.addrs[k] = [2]*py.Object{ho, po}
	return ho, po
}

var (
	serversMu sync.Mutex
	servers   = map[int64]*Server{}
	serverSeq int64
)

// New imports the application ("module:attribute", as uvicorn takes it), starts the
// workers, runs each one's lifespan startup and returns once all of them serve. The
// interpreter must be running, configured with Configure.
func New(app string, opts Options) (*Server, error) {
	if opts.Workers <= 0 {
		opts.Workers = 1
		if py.FreeThreaded() {
			opts.Workers = runtime.GOMAXPROCS(0)
		}
	}
	if opts.Lifespan == "" {
		opts.Lifespan = "auto"
	}
	if opts.BodyChunk <= 0 {
		opts.BodyChunk = 64 << 10
	}
	if opts.HighWater <= 0 {
		opts.HighWater = 64 << 10
	}
	if opts.WebSocketMaxMessage <= 0 {
		opts.WebSocketMaxMessage = 16 << 20
	}
	roots, err := openRoots(opts.PathSendRoots)
	if err != nil {
		return nil, err
	}
	s := &Server{opts: opts, workers: make([]*worker, opts.Workers), reqs: map[int64]*request{},
		attach: make(chan error, opts.Workers), detach: make(chan int, opts.Workers), roots: roots}
	serversMu.Lock()
	serverSeq++
	s.id = serverSeq
	servers[s.id] = s
	serversMu.Unlock()
	err = py.Do(func() error {
		if err := newConstants(); err != nil {
			return err
		}
		m, err := py.Import("pythago_asgi")
		if err != nil {
			return err
		}
		defer m.Close()
		r, err := m.CallMethod("start", s.id, app, opts.Workers, opts.RootPath, opts.Lifespan)
		r.Close()
		return err
	})
	if err != nil {
		s.unregister()
		return nil, err
	}
	var first error
	for range opts.Workers {
		if err := <-s.attach; err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		s.Close(context.Background())
		return nil, first
	}
	return s, nil
}

func (s *Server) unregister() {
	serversMu.Lock()
	delete(servers, s.id)
	serversMu.Unlock()
}

// Close stops accepting requests, runs each worker's lifespan shutdown and stops the
// workers, and returns once their threads have exited (CPython's teardown of their
// thread states included), so py.Finalize may follow at once. Requests still running are
// left to finish or fail on their own; stop the http.Server first (Shutdown) to drain
// them.
func (s *Server) Close(ctx context.Context) error {
	if s.closing.Swap(true) {
		return nil
	}
	n := 0
	for _, w := range s.workers {
		if w != nil {
			w.loop.Post(w.token, py.Tuple{"s"})
			n++
		}
	}
	defer s.unregister()
	defer s.closeRoots()
	for range n {
		select {
		case <-s.detach:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// The workers have detached, but their threads still run CPython's teardown of their
	// thread states. Wait for that too: a worker still tearing down when Py_FinalizeEx
	// runs can have its thread state cleared twice (pythago_asgi.join).
	var timeout any // None: no deadline
	if d, ok := ctx.Deadline(); ok {
		timeout = max(0, time.Until(d).Seconds())
	}
	joined := false
	err := py.Do(func() error {
		m, err := py.Import("pythago_asgi")
		if err != nil {
			return err
		}
		defer m.Close()
		r, err := m.CallMethod("join", s.id, timeout)
		if err != nil {
			return err
		}
		defer r.Close()
		joined, err = r.Truth()
		return err
	})
	switch {
	case err != nil:
		return err
	case !joined && ctx.Err() != nil:
		return ctx.Err()
	case !joined:
		return context.DeadlineExceeded
	}
	return nil
}

func lookupServer(id int64) *Server {
	serversMu.Lock()
	defer serversMu.Unlock()
	return servers[id]
}

// ---- requests ----

type actionKind int

const (
	actStart actionKind = iota
	actBody
	actRead
	actAbort
	actTrailers
	actWSAccept
	actWSSend
	actWSClose
	actPathSend
)

type action struct {
	kind     actionKind
	status   int
	headers  [][2]string // checked: see fields.go
	data     []byte
	obj      *py.Object // the bytes object data aliases; released on the loop thread
	more     bool       // actBody: more body follows; actTrailers: more trailers follow
	trailers bool       // actStart: the response ends with trailers

	file *os.File // actPathSend: the opened file, closed once sent
	size int64

	subprotocol string                // actWSAccept
	msgType     websocket.MessageType // actWSSend
	code        int                   // actWSClose
	reason      string
}

type request struct {
	srv *Server
	h   int64
	wk  *worker

	// sc is the request's scope: the request is its root. Its references count what may
	// still use the request: ServeHTTP until finish, an inline batch until serve_inline
	// took the values (submit), and every _pythago_asgi function running for it
	// (acquire). An HTTP request is recycled when the last is released: by then it is out
	// of srv.reqs, so nothing can take a new reference. A WebSocket's scope is detached
	// (never reused): its reader may outlive the handler.
	sc *arena.Scope[request]
	// mem is what the request borrows for its lifetime, from lanes it keeps across
	// recycling: the packed inline values, the response's header and trailer pairs, its
	// first small body chunk. Takes are lock-free: the goroutines sharing the request
	// (ServeHTTP, the inline holder, _pythago_asgi calls) may take at once.
	mem *requestMem

	mu       sync.Mutex
	queue    []action
	queued   int  // response bytes queued and not yet written
	draining bool // send() waits for queued to fall to the low-water mark
	dead     bool // the handler returned: Python's actions are dropped
	kick     chan struct{}

	offered       bool // trailers were offered (the scope's http.response.trailers)
	trailerFields int  // trailer fields and bytes sent so far, against the limits
	trailerSize   int

	subprotocols []string      // WebSocket: the client's offers
	credit       chan struct{} // WebSocket: messages the reader may pass on

	// The leases of its posted body chunks (bodyLease): the first chunk's, and the
	// current receive() chunk's, free again once the loop thread converted them.
	firstLease, recvLease bodyLease

	// Kept across recycling, so that a typical request allocates none of them:
	in        inlineArgs // inline: serve_inline's values (its bytes are from mem)
	qbuf      [2]action  // queue's first array
	bodyTaken atomic.Bool
}

// requestMem holds a request's lanes.
type requestMem struct {
	bytes arena.SharedLane[byte]      // inline values and the first body chunk
	pairs arena.SharedLane[[2]string] // response header and trailer fields
	// recv is the buffer receive() chunks are read into, one at a time: busy from the
	// read until the loop thread has copied the chunk (its lease's release). A read that
	// finds it busy (receive() called again before the last chunk was converted) uses
	// memory of its own. Kept across recycling up to BodyChunk; recvIdle counts the
	// lifetimes that did not use it, and it is dropped after keepIdle of them.
	recv     []byte
	recvBusy atomic.Bool
	recvUsed bool
	recvIdle int
}

// recvIdleMax is how many lifetimes in a row may leave the receive buffer unused before
// it is dropped.
const recvIdleMax = 16

// bodyLease lends a request's memory (its first chunk in the bytes lane, or the receive
// buffer) to the loop thread's conversion of a posted value: it holds a reference to
// the request, which the conversion releases once it has copied the bytes
// (lease.Bytes), so that the request is not recycled, and its memory reused, before.
// A post never converted (the loop closed) keeps its reference: the request is then
// never recycled and the garbage collector reclaims it.
type bodyLease struct {
	q    *request
	b    []byte
	recv bool // the receive buffer: free it on release
}

func (l *bodyLease) LeasedBytes() []byte { return l.b }

func (l *bodyLease) ReleaseLease() {
	q, recv := l.q, l.recv
	if recv { // after this, the next chunk may take the buffer and this lease
		q.mem.recvBusy.Store(false)
	}
	q.sc.Release()
}

// lend returns b as a value to post that holds a reference to q until the loop thread
// has converted it. lease is q's lease for it (first or recv), free: its last post was
// converted.
func (q *request) lend(lease *bodyLease, b []byte, recv bool) any {
	if len(b) == 0 {
		return constants.empty
	}
	q.sc.Retain()
	*lease = bodyLease{q: q, b: b, recv: recv}
	return lease
}

// readPosted reads a receive() chunk for posting: into the receive buffer, lent until
// the loop thread has converted it, or into memory of its own if the buffer is busy.
func (q *request) readPosted(body io.Reader, size int) (any, bool, error) {
	m := q.mem
	if !m.recvBusy.CompareAndSwap(false, true) {
		data, more, err := readChunk(body, size)
		return data, more, err
	}
	data, more, err := readChunkInto(m.recv[:0], body, size)
	if err != nil {
		m.recvBusy.Store(false)
		return nil, false, err
	}
	if cap(data) <= size+size/2 && cap(data) > cap(m.recv) { // keep what it grew to (append may round up)
		m.recv = data[:0]
	}
	m.recvUsed = true
	if len(data) == 0 {
		m.recvBusy.Store(false)
		return constants.empty, more, nil
	}
	return q.lend(&q.recvLease, data, true), more, nil
}

// requests are the scopes of HTTP requests. Recycling keeps the kick channel (drained),
// the scope, the lanes (reset) and the queue's first array.
var requests = arena.NewPool(func(q *request) {
	select { // a kick nobody took
	case <-q.kick:
	default:
	}
	q.mem.bytes.Reset()
	q.mem.pairs.Reset()
	if m := q.mem; m.recvUsed {
		m.recvUsed, m.recvIdle = false, 0
	} else if m.recv != nil {
		if m.recvIdle++; m.recvIdle >= recvIdleMax {
			m.recv, m.recvIdle = nil, 0
		}
	}
	*q = request{kick: q.kick, sc: q.sc, mem: q.mem}
})

// newRequest registers a request with one reference, ServeHTTP's (finish drops it).
// WebSocket requests are not recycled (pooled false).
func (s *Server) newRequest(wk *worker, offered, pooled bool) *request {
	sc := requests.Get()
	q := &sc.Root
	if q.sc == nil { // a new one
		q.sc, q.kick, q.mem = sc, make(chan struct{}, 1), new(requestMem)
		q.mem.bytes.SetNoZero() // its takers overwrite what they take
	}
	if !pooled {
		sc.Detach()
	}
	q.srv, q.wk, q.offered = s, wk, offered
	// A first body chunk takes up to BodyChunk from the bytes lane (as it grows), next
	// to the inline values: keep a chunk that holds both while requests use it.
	q.mem.bytes.SetKeep(2 * s.opts.BodyChunk)
	q.queue = q.qbuf[:0]
	s.mu.Lock()
	s.seq++
	q.h = s.seq
	s.reqs[q.h] = q
	s.mu.Unlock()
	return q
}

// acquire returns request h with a reference (unref drops it), or nil if it finished.
func (s *Server) acquire(h int64) *request {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.reqs[h]
	if q != nil {
		q.sc.Retain()
	}
	return q
}

// unref drops a reference; the last one recycles an HTTP request (requests). Nothing
// refers to q then: it left srv.reqs in finish (no acquire can find it), its pending
// batch and every _pythago_asgi function that held it are done, and so is ServeHTTP.
// Python never holds it (only h, which no longer resolves), and what serve_inline was
// given was copied.
func (q *request) unref() { q.sc.Release() }

func (q *request) push(a action) {
	q.mu.Lock()
	if q.dead {
		q.mu.Unlock()
		if a.obj != nil {
			a.obj.Close() // called from Python, with a thread state
		}
		if a.file != nil {
			a.file.Close()
		}
		return
	}
	q.queue = append(q.queue, a)
	q.mu.Unlock()
	select {
	case q.kick <- struct{}{}:
	default:
	}
}

// ServeHTTP passes the request to a worker and performs the response it sends.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.closing.Load() {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodConnect && !isWebSocketH2(r) {
		// Proxy tunnels (and other extended CONNECT protocols) are not for ASGI
		// applications.
		http.Error(w, "CONNECT is not supported", http.StatusMethodNotAllowed)
		return
	}
	ws := isWebSocket(r)
	if ws {
		if status, msg := s.checkHandshake(r); status != 0 {
			if status == http.StatusUpgradeRequired {
				w.Header().Set("Sec-WebSocket-Version", "13")
			}
			http.Error(w, msg, status)
			return
		}
	}
	hlen, ok := headerSize(r)
	if !ok {
		http.Error(w, "invalid header value", http.StatusBadRequest)
		return
	}
	wk := s.workers[s.next.Add(1)%uint64(len(s.workers))]
	q := s.newRequest(wk, offerTrailers(r), !ws)
	defer s.finish(q)

	if ws {
		sc := scope(r, hlen)
		q.subprotocols = subprotocols(r)
		q.credit = make(chan struct{}, wsCredits)
		for range wsCredits {
			q.credit <- struct{}{}
		}
		sc[0], sc[1] = "w", q.h
		if r.TLS != nil {
			sc[4] = "wss"
		} else {
			sc[4] = "ws"
		}
		wk.loop.Post(wk.token, append(sc, q.subprotocols))
		s.serveWebSocket(w, r, q, wk)
		return
	}

	// Most request bodies are small: read the first chunk before posting, so that the
	// first receive() needs no round trip.
	var body []byte
	var fused *fusion.Bytes
	left := int64(-1) // the body's bytes not read yet, while known; -1 unknown
	more := false
	if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
		// With the fusion on, the chunk is read into a fused slot, which becomes the
		// bytes object in place. Otherwise it is read into the request's memory, to its
		// declared length. Inline, the queued batch holds the request until serve_inline
		// has copied it into a bytes object; posted to the loop thread, the posted value
		// holds a reference until the loop thread has (bodyLease), as ServeHTTP may
		// return (the client gone) before.
		var err error
		var ok bool
		if fused, body, more, ok, err = readFusedBody(r.Body, r.ContentLength, s.opts.BodyChunk); !ok {
			body, more, err = readFirstChunk(&q.mem.bytes, r.Body, r.ContentLength, s.opts.BodyChunk)
		}
		if r.ContentLength > 0 {
			left = r.ContentLength - int64(len(body))
		}
		if err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
	}
	if wk.serve != nil {
		q.prepare(r, hlen, body, more)
		q.in.fused = fused // released after serve_inline unless converted
		wk.submit(q)
	} else {
		sc := scope(r, hlen)
		sc[1] = q.h
		// A fused body needs no lease: its slot is not the request's memory. A post never
		// converted (the loop closed) keeps its slot, as a lease keeps its request.
		var b any = fused
		if fused == nil {
			b = q.lend(&q.firstLease, body, false)
		}
		wk.loop.Post(wk.token, append(sc, b, more, q.offered))
	}

	// Request trailers (r.Trailer) are never passed on: ASGI has no place for them, and
	// merging them into the headers is a known smuggling vector.
	ctx := r.Context()
	status := 0
	trailers := false // the final body is followed by trailers
	for {
		select {
		case <-q.kick:
		case <-ctx.Done():
			wk.loop.Post(wk.token, py.Tuple{"x", q.h})
			return
		}
		q.mu.Lock()
		acts := q.queue
		q.queue = nil
		q.mu.Unlock()
		for i, a := range acts {
			switch a.kind {
			case actStart:
				hdr := w.Header()
				vals := make([]string, len(a.headers)) // the new fields' values, one allocation
				for i, kv := range a.headers {
					k := http.CanonicalHeaderKey(kv[0])
					if old := hdr[k]; old != nil {
						hdr[k] = append(old, kv[1])
					} else {
						vals[i] = kv[1]
						hdr[k] = vals[i : i+1 : i+1]
					}
				}
				status = a.status
				trailers = a.trailers
			case actBody:
				if status != 0 {
					w.WriteHeader(status)
					status = 0
					if trailers {
						// Send the head now: with no length known, net/http commits to
						// chunked encoding (HTTP/1.1), which trailers need; otherwise a
						// short body would get a computed Content-Length.
						if f, ok := w.(http.Flusher); ok {
							f.Flush()
						}
					}
				}
				var err error
				if len(a.data) > 0 {
					_, err = w.Write(a.data)
				}
				if a.obj != nil {
					wk.release(a.obj)
				}
				q.written(len(a.data))
				if err != nil {
					releaseRest(wk, acts[i+1:])
					wk.loop.Post(wk.token, py.Tuple{"x", q.h})
					return
				}
				if !a.more && !trailers {
					releaseRest(wk, acts[i+1:])
					return
				}
				if f, ok := w.(http.Flusher); ok && a.more {
					f.Flush()
				}
			case actTrailers:
				// Set after the head is written, with TrailerPrefix: net/http sends these
				// as trailers (HTTP/1.1 chunked trailer section, HTTP/2 trailing HEADERS).
				hdr := w.Header()
				for _, kv := range a.headers {
					k := http.TrailerPrefix + http.CanonicalHeaderKey(kv[0])
					hdr[k] = append(hdr[k], kv[1])
				}
				if !a.more {
					releaseRest(wk, acts[i+1:])
					return
				}
			case actPathSend:
				if status != 0 {
					w.WriteHeader(status)
					status = 0
				}
				var err error
				if r.Method != http.MethodHead {
					err = sendFile(w, a.file, a.size)
				} else {
					a.file.Close()
				}
				if err != nil {
					releaseRest(wk, acts[i+1:])
					panic(http.ErrAbortHandler) // the declared length cannot be met
				}
				if !trailers {
					releaseRest(wk, acts[i+1:])
					return
				}
			case actRead:
				if fb, data, more, ok, err := readFusedBody(r.Body, left, s.opts.BodyChunk); ok {
					if err != nil {
						wk.loop.Post(wk.token, py.Tuple{"x", q.h})
						releaseRest(wk, acts[i+1:])
						return
					}
					if left >= 0 {
						left -= int64(len(data))
					}
					if fb != nil {
						wk.loop.Post(wk.token, py.Tuple{"b", q.h, fb, more})
					} else {
						wk.loop.Post(wk.token, py.Tuple{"b", q.h, data, more})
					}
					break
				}
				left = -1 // read past this point unaccounted: no longer tracked
				data, more, err := q.readPosted(r.Body, s.opts.BodyChunk)
				if err != nil {
					wk.loop.Post(wk.token, py.Tuple{"x", q.h})
					releaseRest(wk, acts[i+1:])
					return
				}
				wk.loop.Post(wk.token, py.Tuple{"b", q.h, data, more})
			case actAbort:
				releaseRest(wk, acts[i+1:])
				panic(http.ErrAbortHandler)
			}
		}
		// All done: the next actions can go into this array again.
		clear(acts)
		q.mu.Lock()
		if q.queue == nil {
			q.queue = acts[:0]
		}
		q.mu.Unlock()
	}
}

func releaseRest(wk *worker, acts []action) {
	for _, a := range acts {
		if a.obj != nil {
			wk.release(a.obj)
		}
		if a.file != nil {
			a.file.Close()
		}
	}
}

// finish drops the request: later actions from Python are discarded.
func (s *Server) finish(q *request) {
	q.mu.Lock()
	q.dead = true
	rest := q.queue
	q.queue = nil
	q.mu.Unlock()
	releaseRest(q.wk, rest)
	s.mu.Lock()
	delete(s.reqs, q.h)
	s.mu.Unlock()
	q.unref()
}

// written accounts for body bytes sent, and tells a waiting send() when enough drained.
func (q *request) written(n int) {
	q.mu.Lock()
	q.queued -= n
	wake := q.draining && q.queued <= q.srv.opts.HighWater/4
	if wake {
		q.draining = false
	}
	q.mu.Unlock()
	if wake {
		q.wk.loop.Post(q.wk.token, py.Tuple{"d", q.h})
	}
}

// readFirstChunk is readChunk for the request's first chunk, whose length is known when
// declared (n >= 0): a body that fits one chunk is read to its length, where net/http
// ends it, without the buffer growth that seeing its EOF with the rest of the chunk's
// room took; there is no more. The buffer grows as data arrives, as readChunk's does;
// with a lane, its steps are taken from it.
func readFirstChunk(lane *arena.SharedLane[byte], body io.Reader, n int64, size int) ([]byte, bool, error) {
	limit, known := size, n > 0 && n <= int64(size)
	if known {
		limit = int(n)
	}
	grow := func(buf []byte, want int) []byte {
		if lane == nil {
			return slices.Grow(buf, want)
		}
		if len(buf) > 0 { // still the lane's last take: grow it in place
			if more, ok := lane.Extend(buf[:cap(buf)], want); ok {
				return more[:len(buf)]
			}
		}
		next := lane.Take(len(buf) + want)[:len(buf)]
		copy(next, buf)
		return next
	}
	buf := grow(nil, min(limit, 4<<10))
	for len(buf) < limit {
		if len(buf) == cap(buf) {
			buf = grow(buf, min(cap(buf), limit-len(buf)))
		}
		m, err := body.Read(buf[len(buf):min(cap(buf), limit)])
		buf = buf[:len(buf)+m]
		switch {
		case errors.Is(err, io.EOF):
			return buf, false, nil
		case err != nil:
			return nil, false, err
		}
	}
	if known {
		// Read on to the body's EOF, as readChunk does: net/http starts its background
		// read of the connection (which notices a client gone) when a body hits EOF.
		// The probe needs no room in buf: the body ends at its declared length.
		probe := grow(nil, 1)[:1]
		if m, err := body.Read(probe); m > 0 {
			return nil, false, errors.New("asgi: the request body is longer than its Content-Length")
		} else if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		return buf, false, nil
	}
	return buf, true, nil // a full chunk of a longer or unknown body: possibly more
}

// readChunk reads up to size bytes of body, and reports whether there may be more. The
// buffer grows as data arrives, so a client that declares a large body and sends little
// costs little.
func readChunk(body io.Reader, size int) ([]byte, bool, error) {
	return readChunkInto(nil, body, size)
}

// readChunkInto is readChunk reading into buf's array while it has room.
func readChunkInto(buf []byte, body io.Reader, size int) ([]byte, bool, error) {
	if cap(buf) == 0 {
		buf = make([]byte, 0, min(size, 4<<10))
	}
	for len(buf) < size {
		if len(buf) == cap(buf) {
			buf = slices.Grow(buf, min(cap(buf), size-len(buf)))
		}
		n, err := body.Read(buf[len(buf):min(cap(buf), size)])
		buf = buf[:len(buf)+n]
		switch {
		case errors.Is(err, io.EOF):
			return buf, false, nil
		case err != nil:
			return nil, false, err
		}
	}
	return buf, true, nil // possibly more
}

// scope is what the worker builds the ASGI scope from when ServeHTTP posts a request to
// its loop thread (Options.LoopThread, WebSockets):
// ("r", h, method, http_version, scheme, raw_path, path, query_string, packed headers,
// client, server); ServeHTTP sets h and appends body, more_body and whether trailers are
// offered. hlen is headerSize(r).
func scope(r *http.Request, hlen int) py.Tuple {
	version, scheme := protocol(r)
	return py.Tuple{"r", int64(0), constant(r.Method), constant(version), constant(scheme),
		[]byte(r.URL.EscapedPath()), validPath(r.URL.Path), []byte(r.URL.RawQuery),
		packHeaders(make([]byte, 0, hlen), r), addr(r.RemoteAddr), localAddr(r)}
}

// protocol returns the scope's http_version and scheme.
func protocol(r *http.Request) (version, scheme string) {
	version = "1.1"
	switch {
	case r.ProtoMajor == 2:
		version = "2"
	case r.ProtoMajor == 1 && r.ProtoMinor == 0:
		version = "1.0"
	}
	scheme = "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return version, scheme
}

func validPath(path string) string {
	if !utf8.ValidString(path) {
		path = strings.ToValidUTF8(path, "\uFFFD")
	}
	return path
}

// Headers travel as one "name\0value\0..." bytes object (HTTP forbids NUL in both), which
// Python splits in C: far fewer calls than a list of tuples built from Go.

// headerSize returns the size of r's packed headers. It reports false for a header value
// with NUL, which the packing cannot carry (net/http rejects such requests already; this
// does not rely on it), and for a Host field that is not r.Host.
func headerSize(r *http.Request) (int, bool) {
	n := len("host") + 1 + len(r.Host)
	if strings.IndexByte(r.Host, 0) >= 0 {
		return 0, false
	}
	// The host is r.Host, as net/http routes it. HTTP/1.1 moves the Host header there;
	// HTTP/2 leaves a host field in the headers next to :authority, and one that differs
	// from it makes the request malformed (RFC 9113 §8.3.1): refused, rather than handing
	// the application another host than Go routed by.
	for _, h := range r.Header.Values("Host") {
		if h != r.Host {
			return 0, false
		}
	}
	for k, vs := range r.Header {
		if k == "Host" {
			continue
		}
		for _, v := range vs {
			if strings.IndexByte(k, 0) >= 0 || strings.IndexByte(v, 0) >= 0 {
				return 0, false
			}
			n += 2 + len(k) + len(v)
		}
	}
	return n, true
}

// packHeaders appends r's headers, packed (headerSize checked them).
func packHeaders(headers []byte, r *http.Request) []byte {
	headers = append(append(append(headers, "host"...), 0), r.Host...)
	for k, vs := range r.Header {
		if k == "Host" {
			continue
		}
		for _, v := range vs {
			headers = append(headers, 0)
			for i := 0; i < len(k); i++ { // lower-case ASCII, as ASGI wants
				c := k[i]
				if 'A' <= c && c <= 'Z' {
					c += 'a' - 'A'
				}
				headers = append(headers, c)
			}
			headers = append(append(headers, 0), v...)
		}
	}
	return headers
}

// splitAddr splits "host:port"; ok is false if it is not one.
func splitAddr(hostport string) (host string, port int, ok bool) {
	host, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", 0, false
	}
	port, _ = strconv.Atoi(p)
	return host, port, true
}

func addr(hostport string) any {
	host, port, ok := splitAddr(hostport)
	if !ok {
		return nil
	}
	return py.Tuple{host, port}
}

func localAddr(r *http.Request) any {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		return addr(a.String())
	}
	return nil
}

// startAction takes http.response.start: the status, the headers packed by Python as one
// "name\0value\0..." bytes object with their count, and whether trailers follow.
func startAction(q *request, status, headers, count, trailers *py.Object) (action, error) {
	code, err := status.Int64()
	if err != nil {
		return action{}, err
	}
	if code < 200 || code > 599 {
		// 1xx would be an interim response (101 would switch protocols); others make
		// net/http panic.
		return action{}, fmt.Errorf("invalid ASGI response status %d", code)
	}
	t, err := trailers.Truth()
	if err != nil {
		return action{}, err
	}
	if t && !q.offered {
		return action{}, errors.New("trailers were not offered for this request (no http.response.trailers extension)")
	}
	pairs, err := packedFieldsInto(&q.mem.pairs, headers, count, "headers")
	if err != nil {
		return action{}, err
	}
	if err := checkResponseHeaders(pairs, t); err != nil {
		return action{}, err
	}
	return action{kind: actStart, status: int(code), headers: pairs, trailers: t}, nil
}

func packedFields(packed, count *py.Object, what string) ([][2]string, error) {
	return packedFieldsInto(nil, packed, count, what)
}

// packedFieldsInto is packedFields taking the pairs from lane (a request's, if not nil).
func packedFieldsInto(lane *arena.SharedLane[[2]string], packed, count *py.Object, what string) ([][2]string, error) {
	b, err := packed.UnsafeBytes()
	if err != nil {
		return nil, err
	}
	n, err := count.Int64()
	if err != nil {
		return nil, err
	}
	return parseFields(lane, b, n, what)
}

// trailersAction takes http.response.trailers (Python checked the message order).
func trailersAction(q *request, packed, count, more *py.Object) (action, error) {
	pairs, err := packedFieldsInto(&q.mem.pairs, packed, count, "trailers")
	if err != nil {
		return action{}, err
	}
	m, err := more.Truth()
	if err != nil {
		return action{}, err
	}
	q.mu.Lock()
	err = checkTrailers(pairs, &q.trailerFields, &q.trailerSize)
	q.mu.Unlock()
	if err != nil {
		return action{}, err
	}
	return action{kind: actTrailers, headers: pairs, more: m}, nil
}

// bodyAction takes a body chunk; bytes are written from the object itself (no copy).
func bodyAction(q *request, data, more *py.Object) (action, error) {
	m, err := more.Truth()
	if err != nil {
		return action{}, err
	}
	act := action{kind: actBody, more: m}
	if b, err := data.UnsafeBytes(); err == nil {
		switch {
		case len(b) > maxCopiedBody:
			act.data, act.obj = b, data.NewRef()
		case len(b) > 0:
			// Copying a small body is cheaper than holding the object: a reference,
			// a GC cleanup and a release on the worker's thread later. The first chunk
			// is copied into the request's memory; later ones (a stream may send many)
			// into memory of their own, which the request would keep until it ends.
			if q.bodyTaken.CompareAndSwap(false, true) {
				act.data = q.mem.bytes.Take(len(b))
				copy(act.data, b)
			} else {
				act.data = append([]byte(nil), b...)
			}
		}
	} else if act.data, err = data.Bytes(); err != nil { // bytearray, memoryview: copied
		return action{}, err
	}
	return act, nil
}

// maxCopiedBody is the largest response body chunk copied rather than written from the
// Python object itself.
const maxCopiedBody = 4 << 10

// queueBody queues a body chunk and reports whether send() should wait for the client.
func (q *request) queueBody(act action) bool {
	q.mu.Lock()
	q.queued += len(act.data)
	wait := act.more && q.queued > q.srv.opts.HighWater
	if wait {
		q.draining = true
	}
	q.mu.Unlock()
	q.push(act)
	return wait
}

// ---- the _pythago_asgi module ----

func module() map[string]any {
	srvOf := func(o *py.Object) *Server {
		id, _ := o.Int64()
		return lookupServer(id)
	}
	// reqOf returns the request with a reference: the caller must unref it.
	reqOf := func(a []*py.Object) *request {
		s := srvOf(a[0])
		if s == nil {
			return nil
		}
		h, _ := a[1].Int64()
		q := s.acquire(h)
		if q != nil && s.onCall != nil {
			s.onCall(q)
		}
		return q
	}
	return map[string]any{
		// attach(server, index, loop_id, token, serve_inline): worker index serves. Called
		// on the worker's loop thread.
		"attach": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			s := srvOf(a[0])
			i, _ := a[1].Int64()
			id, _ := a[2].Int64()
			token, _ := a[3].Int64()
			l := aio.LookupLoop(id)
			if s == nil || l == nil {
				return nil, errors.New("pythago_asgi: unknown server or loop")
			}
			w := &worker{loop: l, token: token}
			if !s.opts.LoopThread {
				w.serve, w.busy = a[4].NewRef(), true // the loop thread holds it until it waits
				l.SetHooks(w)
			}
			s.workers[i] = w
			s.attach <- nil
			return nil, nil
		}),
		// failed(server, index, message): the worker could not start.
		"failed": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			if s := srvOf(a[0]); s != nil {
				msg, _ := a[2].GoString()
				s.attach <- errors.New("asgi: worker failed to start: " + msg)
			}
			return nil, nil
		}),
		// detach(server, index): the worker stopped.
		"detach": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			if s := srvOf(a[0]); s != nil {
				i, _ := a[1].Int64()
				s.detach <- int(i)
			}
			return nil, nil
		}),
		// start(server, h, status, headers, count, trailers)
		"start": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			q := reqOf(a)
			if q == nil {
				return nil, nil
			}
			defer q.unref()
			st, err := startAction(q, a[2], a[3], a[4], a[5])
			if err != nil {
				return nil, err
			}
			q.push(st)
			return nil, nil
		}),
		// body(server, h, data, more) -> whether send() should wait for the client
		"body": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			q := reqOf(a)
			if q == nil {
				return false, nil
			}
			defer q.unref()
			b, err := bodyAction(q, a[2], a[3])
			if err != nil {
				return nil, err
			}
			return q.queueBody(b), nil
		}),
		// respond(server, h, status, headers, count, trailers, data, more): start and the
		// first body in one call, which is how most responses go.
		"respond": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			q := reqOf(a)
			if q == nil {
				return false, nil
			}
			defer q.unref()
			st, err := startAction(q, a[2], a[3], a[4], a[5])
			if err != nil {
				return nil, err
			}
			b, err := bodyAction(q, a[6], a[7])
			if err != nil {
				return nil, err
			}
			q.push(st)
			return q.queueBody(b), nil
		}),
		// trailers(server, h, headers, count, more_trailers)
		"trailers": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			q := reqOf(a)
			if q == nil {
				return nil, nil
			}
			defer q.unref()
			t, err := trailersAction(q, a[2], a[3], a[4])
			if err != nil {
				return nil, err
			}
			q.push(t)
			return nil, nil
		}),
		// ws_accept(server, h, subprotocol, headers, count)
		"ws_accept": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			q := reqOf(a)
			if q == nil {
				return nil, nil
			}
			defer q.unref()
			act, err := wsAcceptAction(q, a[2], a[3], a[4])
			if err != nil {
				return nil, err
			}
			q.push(act)
			return nil, nil
		}),
		// ws_send(server, h, is_text, data) -> whether send() should wait for the client
		"ws_send": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			q := reqOf(a)
			if q == nil {
				return false, nil
			}
			defer q.unref()
			act, err := wsSendAction(a[2], a[3])
			if err != nil {
				return nil, err
			}
			act.more = true // for queueBody's accounting
			return q.queueBody(act), nil
		}),
		// ws_close(server, h, code, reason); before accept, a 403
		"ws_close": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			q := reqOf(a)
			if q == nil {
				return nil, nil
			}
			defer q.unref()
			act, err := wsCloseAction(a[2], a[3])
			if err != nil {
				return nil, err
			}
			q.push(act)
			return nil, nil
		}),
		// ws_ack(server, h): the application took a message; the reader may read another.
		"ws_ack": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			if q := reqOf(a); q != nil {
				if q.credit != nil {
					select {
					case q.credit <- struct{}{}:
					default:
					}
				}
				q.unref()
			}
			return nil, nil
		}),
		// pathsend(server, h, status, headers, count, trailers, path): the held head and a
		// file as the whole body.
		"pathsend": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			q := reqOf(a)
			if q == nil {
				return nil, nil
			}
			defer q.unref()
			st, ps, err := pathSendActions(q, a)
			if err != nil {
				return nil, err
			}
			q.push(st)
			q.push(ps)
			return nil, nil
		}),
		// want_body(server, h): read the next request body chunk ("b" follows).
		"want_body": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			if q := reqOf(a); q != nil {
				q.push(action{kind: actRead})
				q.unref()
			}
			return nil, nil
		}),
		// abort(server, h): drop the connection (the response cannot be completed).
		"abort": py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			if q := reqOf(a); q != nil {
				q.push(action{kind: actAbort})
				q.unref()
			}
			return nil, nil
		}),
	}
}
