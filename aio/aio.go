// Package aio runs asyncio event loops on Go's runtime. Python code imports goaio:
//
//   - goaio.GoEventLoop (proactor): asyncio's own scheduling (ready queue, timers, Tasks,
//     Futures), while goroutines do the I/O with Go's net package, name resolution
//     included (no executor threads), and queue completions for the loop. Pipes
//     (connect_read_pipe, connect_write_pipe) and asyncio subprocesses go through the same
//     goroutines; a child's exit is reaped in Go (wait4). TLS (create_connection and
//     create_server with ssl=) is done by crypto/tls when the ssl.SSLContext maps
//     faithfully, which goaio_tls decides from what the context was configured with (aio's
//     import hook patches ssl.SSLContext to record it as ssl is imported); otherwise, and
//     for start_tls, by asyncio's own SSLProtocol over the Go connection. Verification on
//     the Go path follows the context, not Go's defaults: the trust anchors it loaded
//     (cafile, capath, cadata such as certifi's, the default verify paths), verify_mode,
//     check_hostname with OpenSSL's common-name fallback, and failures raise
//     ssl.SSLCertVerificationError with OpenSSL's verify codes. add_reader and add_writer
//     (zmq.asyncio, for one) watch the fd as GoSelectorEventLoop does, and the readiness
//     comes back in the loop's completion queue; the sock_* methods are not there.
//   - goaio.GoSelectorEventLoop: asyncio.SelectorEventLoop over a selector whose
//     registrations are goroutines parked on Go's netpoller. Python makes its own socket
//     calls, so TLS, UDP, pipes and the sock_* APIs work unchanged.
//   - goaio.run(coro, selector=False): asyncio.run with one of them.
//
// A loop waits for Go in a Go function with its thread state released (py.Release): only
// that Python thread's OS thread blocks, as it would in epoll_wait. Goroutines queue their
// results in a mutex-protected batch and wake the loop on an empty→non-empty transition.
package aio

import (
	"context"
	_ "embed"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/moriyoshi/pythago/py"
)

//go:embed goaio.py
var source string

//go:embed goaio_tls.py
var tlsSource string

// finder makes the modules in _goaio.SOURCES (goaio, and those added with AddSource)
// importable, with their source available to tracebacks (linecache asks the loader).
// It runs in a namespace of its own.
const finder = `
import sys

class _GoaioLoader:
    @classmethod
    def find_spec(cls, name, path=None, target=None):
        import _goaio
        if name in _goaio.SOURCES:
            from importlib.machinery import ModuleSpec
            return ModuleSpec(name, cls, origin="<pythago>/%s.py" % name)

    @staticmethod
    def create_module(spec):
        return None

    @staticmethod
    def exec_module(module):
        module.__file__ = module.__spec__.origin
        exec(compile(_GoaioLoader.get_source(module.__name__), module.__file__, "exec"), module.__dict__)

    @staticmethod
    def get_source(name):
        import _goaio
        return _goaio.SOURCES[name]

sys.meta_path.append(_GoaioLoader)


class _GoaioSSLHook:
    """Records ssl.SSLContext configurations (goaio_tls) from the moment ssl is imported,
    so GoEventLoop can do TLS in Go when a context allows it. Costs nothing until then."""

    @classmethod
    def find_spec(cls, name, path=None, target=None):
        if name != "ssl":
            return None
        for finder in sys.meta_path:
            find = None if finder is cls else getattr(finder, "find_spec", None)
            spec = find(name, path, target) if find is not None else None
            if spec is not None:
                break
        else:
            return None
        loader = spec.loader

        class _Loader:
            @staticmethod
            def create_module(spec):
                return loader.create_module(spec)

            @staticmethod
            def exec_module(module):
                module.__spec__.loader = module.__loader__ = loader
                loader.exec_module(module)
                import goaio_tls
                goaio_tls.record(module)

        spec.loader = _Loader
        return spec


sys.meta_path.insert(0, _GoaioSSLHook)
`

var sources = map[string]string{"goaio": source, "goaio_tls": tlsSource}

// AddSource makes a Python module importable by name from source, through the same import
// hook as goaio. Call it before Configure; packages built on aio (asgi) use it.
func AddSource(name, src string) { sources[name] = src }

// Configure makes goaio importable in the interpreter cfg configures, and in processes
// started from sys.executable (multiprocessing workers). It adds the _goaio module to
// cfg.Modules, and an import hook to cfg.Setup (running after any existing Setup).
// Configuring the same Config twice is harmless.
// Go channels passed to Python become awaitables for goaio.GoEventLoop; their
// buffering, direction and close state are those of the original Go channel.
func Configure(cfg *py.Config) {
	py.RegisterChannelConverter(channelObject)
	if cfg.Modules == nil {
		cfg.Modules = map[string]map[string]any{}
	}
	if _, ok := cfg.Modules["_goaio"]; ok {
		cfg.Modules["_goaio"]["SOURCES"] = sources
		return
	}
	cfg.Modules["_goaio"] = module()
	setup := cfg.Setup
	cfg.Setup = func() error {
		if setup != nil {
			if err := setup(); err != nil {
				return err
			}
		}
		ns, err := py.From(map[string]any{})
		if err != nil {
			return err
		}
		defer ns.Close()
		return py.ExecIn(finder, ns)
	}
}

// ---- argument helpers ----

// Arguments come from goaio.py, so a mistyped one is a bug there: panicking raises
// RuntimeError in Python.
func intArg(a []*py.Object, i int) int64 {
	n, err := a[i].Int64()
	if err != nil {
		panic(err)
	}
	return n
}

func strArg(a []*py.Object, i int) string {
	s, err := a[i].GoString()
	if err != nil {
		panic(err)
	}
	return s
}

// timeoutArg converts a select() timeout: None waits forever (-1).
func timeoutArg(o *py.Object) time.Duration {
	if o.IsNone() {
		return -1
	}
	f, err := o.Float64()
	if err != nil {
		panic(err)
	}
	if f <= 0 {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}

func addrTuple(a net.Addr) any {
	if ta, ok := a.(*net.TCPAddr); ok {
		return py.Tuple{ta.IP.String(), ta.Port}
	}
	return nil
}

// errnoOf maps err to an errno, or to a negative EAI_* code (Linux's values) for a name
// resolution failure, which goaio.py raises as socket.gaierror with this platform's code.
func errnoOf(err error) (int64, string) {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return -2, dnsErr.Error() // EAI_NONAME
		case dnsErr.IsTemporary || dnsErr.IsTimeout:
			return -3, dnsErr.Error() // EAI_AGAIN
		}
		return -4, dnsErr.Error() // EAI_FAIL
	}
	var en syscall.Errno
	if errors.As(err, &en) {
		return int64(en), en.Error()
	}
	return int64(syscall.EIO), err.Error()
}

// oserror is the (None, errno, message) result goaio.py raises as OSError.
func oserror(err error) py.Tuple {
	errno, msg := errnoOf(err)
	return py.Tuple{nil, errno, msg}
}

// ---- handles ----

var (
	handleMu sync.Mutex
	handles  = map[int64]any{}
	nextH    int64
)

func register(v any) int64 {
	handleMu.Lock()
	defer handleMu.Unlock()
	nextH++
	handles[nextH] = v
	return nextH
}

func lookup[T any](h int64) T {
	handleMu.Lock()
	defer handleMu.Unlock()
	v, _ := handles[h].(T)
	return v
}

func unregister(h int64) {
	handleMu.Lock()
	delete(handles, h)
	handleMu.Unlock()
}

// ---- wakeable batch ----

// batch is a mutex-protected queue that one loop thread drains. Producers wake the loop
// only when the batch goes from empty to non-empty, and block (backpressure) above
// maxBatch until the loop drains.
type batch[T any] struct {
	wake  chan struct{}
	mu    sync.Mutex
	cond  *sync.Cond
	items []T
	hooks Hooks // set once, on the loop thread (Loop.SetHooks)
}

const maxBatch = 1 << 16

func newBatch[T any]() *batch[T] {
	b := &batch[T]{wake: make(chan struct{}, 1)}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (b *batch[T]) post(v T) {
	b.mu.Lock()
	for len(b.items) >= maxBatch {
		b.cond.Wait()
	}
	wasEmpty := len(b.items) == 0
	b.items = append(b.items, v)
	b.mu.Unlock()
	if wasEmpty {
		kick(b.wake)
	}
}

func (b *batch[T]) take() []T {
	b.mu.Lock()
	items := b.items
	b.items = nil
	b.cond.Broadcast()
	b.mu.Unlock()
	return items
}

// wait drains the batch, waiting up to timeout (forever if negative) with the thread
// state released when it is empty. A wake is never discarded unconsumed: wakeup kicks
// without queueing anything (call_soon_threadsafe), so a kick from just before the call
// is the only sign of work. A wake left over from an earlier drain at worst returns early
// with nothing, which asyncio treats as a timeout.
//
// With hooks (Loop.SetHooks), the loop thread serves the hooks' work at every poll, and
// unlocks them before it waits and locks them again, with the thread state released,
// before it returns to Python.
func (b *batch[T]) wait(timeout time.Duration) []T {
	h := b.hooks
	if h != nil {
		h.Serve()
	}
	if items := b.take(); len(items) > 0 || timeout == 0 {
		return items
	}
	if h != nil {
		h.Unlock()
	}
	py.Release(func() {
		defer func() {
			if h != nil {
				h.Lock()
			}
		}()
		if timeout < 0 {
			<-b.wake
			return
		}
		t := time.NewTimer(timeout)
		select {
		case <-b.wake:
		case <-t.C:
		}
		t.Stop()
	})
	return b.take()
}

// ---- proactor loop (GoEventLoop) ----

// Completion kinds; must match goaio.py.
const (
	evConnected = iota
	evData
	evEOF
	evError
	evAccepted
	evResolved
	evDrained
	evClosed
	evFilled // a BufferedProtocol read: n bytes in the protocol's buffer
	evGo     // a Go value for a callback, from Loop.Post
	evExited // a child process exited: n is its exit code
	evReady  // an fd watched for a reader or writer (add_reader) is ready: val is its selEvent
)

type completion struct {
	token int64
	kind  int
	val   any // evGo
	n     int64
	data  []byte
	pool  *[]byte
	held  chan struct{} // evData: the connection's credit, handed back by poll
	msg   string
	addrs []string
}

type aioLoop struct {
	channelClosed bool // protected by batch.mu
	*batch[completion]
	doneMu sync.Mutex
	done   []interface{ Close() } // written bytes objects and filled buffers, closed on the loop thread
	handed []selEvent             // readiness the last poll returned (evReady), re-armed by the next
}

// release hands objects and buffers that goroutines are done with to the loop thread,
// which closes them (that needs a thread state) at its next poll, before the completions
// it then returns.
func (l *aioLoop) release(objs ...interface{ Close() }) {
	l.doneMu.Lock()
	l.done = append(l.done, objs...)
	l.doneMu.Unlock()
}

func (l *aioLoop) closeDone() {
	l.doneMu.Lock()
	objs := l.done
	l.done = nil
	l.doneMu.Unlock()
	for _, o := range objs {
		o.Close()
	}
}

// readReq is a BufferedProtocol read: the protocol's buffer, exported, and its memory.
type readReq struct {
	buf  *py.Buffer
	data []byte
}

var readBufs = sync.Pool{New: func() any { b := make([]byte, 256<<10); return &b }}

// dataInFlight is how many of a connection's EV_DATA events may wait for the loop thread
// at once; the reader then holds the next read until poll has copied one out, and the
// data waits in the socket, where TCP pushes back on the sender. pause_reading alone is
// not enough: Python must run to call it, and with GOMAXPROCS=1 the reader goroutine can
// hold the only P for a whole time slice, queueing about 100 buffers (25 MB) that the
// StreamReader then takes at once (1-core TLS bulk: 1.2 GB/s and 350k page faults per
// GB, as glibc grew and trimmed those megabytes on every round).
var dataInFlight = 1

type aioConn struct {
	loop       *aioLoop
	c          net.Conn
	token      atomic.Int64
	mu         sync.Mutex
	queue      [][]byte
	objs       []*py.Object
	queued     int64
	low        int64
	closing    bool
	eofWanted  bool
	wake       chan struct{}
	aborted    atomic.Bool
	paused     atomic.Bool
	resume     chan struct{}
	readerOn   atomic.Bool
	readerDone chan struct{}
	stop       chan struct{}   // closed by the writer when the connection is done
	reads      chan readReq    // BufferedProtocol reads, one at a time
	credits    chan struct{}   // EV_DATA events posted and not yet polled (dataInFlight)
	rbuf       py.Buffer       // reused by every read_into: one read is in flight at a time
	rc         syscall.RawConn // the descriptor's, set by newConn (nil for TLS)
	nb         *nbConn         // under a TLS connection, set by newConn
}

func newConn(l *aioLoop, c net.Conn) int64 {
	ac := &aioConn{loop: l, c: c, wake: make(chan struct{}, 1), resume: make(chan struct{}, 1),
		readerDone: make(chan struct{}), stop: make(chan struct{}), reads: make(chan readReq, 1),
		credits: make(chan struct{}, dataInFlight)}
	ac.coalescing()
	h := register(ac)
	go ac.writer()
	return h
}

func (ac *aioConn) writer() {
	eofDone := false
	for {
		ac.mu.Lock()
		for len(ac.queue) == 0 && !ac.closing && !(ac.eofWanted && !eofDone) && !ac.aborted.Load() {
			ac.mu.Unlock()
			<-ac.wake
			ac.mu.Lock()
		}
		if ac.aborted.Load() {
			releaseObjects(ac.loop, ac.objs)
			ac.queue, ac.objs = nil, nil
			ac.mu.Unlock()
			break
		}
		if len(ac.queue) > 0 {
			bufs := net.Buffers(ac.queue)
			objs := ac.objs
			ac.queue, ac.objs = nil, nil
			ac.mu.Unlock()
			n, err := bufs.WriteTo(ac.c) // writev, straight from the bytes objects
			releaseObjects(ac.loop, objs)
			ac.mu.Lock()
			before := ac.queued
			ac.queued -= n
			after, low := ac.queued, ac.low
			ac.mu.Unlock()
			if err != nil {
				if !ac.aborted.Load() {
					errno, msg := errnoOf(err)
					ac.loop.post(completion{token: ac.token.Load(), kind: evError, n: errno, msg: msg})
				}
				break
			}
			if before > low && after <= low {
				ac.loop.post(completion{token: ac.token.Load(), kind: evDrained, n: after})
			}
			continue
		}
		if ac.eofWanted && !eofDone {
			ac.mu.Unlock()
			eofDone = true
			if cw, ok := ac.c.(interface{ CloseWrite() error }); ok { // TCP, a pipe
				cw.CloseWrite()
			}
			continue
		}
		ac.mu.Unlock()
		break
	}
	ac.c.Close()
	close(ac.stop) // a paused reader, or one waiting for a buffer or a credit, stops too
	if ac.readerOn.Load() {
		<-ac.readerDone
	}
	ac.loop.post(completion{token: ac.token.Load(), kind: evClosed})
}

func releaseObjects(l *aioLoop, objs []*py.Object) {
	cs := make([]interface{ Close() }, len(objs))
	for i, o := range objs {
		cs[i] = o
	}
	l.release(cs...)
}

func (ac *aioConn) reader() {
	defer close(ac.readerDone)
	for {
		for ac.paused.Load() && !ac.aborted.Load() {
			select {
			case <-ac.resume:
			case <-ac.stop:
				return
			}
		}
		pb := readBufs.Get().(*[]byte)
		n, err := ac.c.Read(*pb)
		if n >= coalesceMin && err == nil {
			var m int
			m, err = ac.readMore((*pb)[n:])
			n += m
		}
		if n > 0 {
			select {
			case ac.credits <- struct{}{}:
			case <-ac.stop:
				readBufs.Put(pb)
				return
			}
			ac.loop.post(completion{token: ac.token.Load(), kind: evData, data: (*pb)[:n], pool: pb, held: ac.credits})
		} else {
			readBufs.Put(pb)
		}
		if err != nil {
			ac.mu.Lock()
			closing := ac.closing
			ac.mu.Unlock()
			switch {
			case errors.Is(err, io.EOF):
				ac.loop.post(completion{token: ac.token.Load(), kind: evEOF})
			case closing || ac.aborted.Load():
			default:
				errno, msg := errnoOf(err)
				ac.loop.post(completion{token: ac.token.Load(), kind: evError, n: errno, msg: msg})
			}
			return
		}
	}
}

// bufferedReader serves a BufferedProtocol: each read goes straight into the buffer the
// protocol's get_buffer returned, which the loop exported (read_into). The export is handed
// back to the loop thread before the completion, so it is released by the time Python sees
// buffer_updated.
func (ac *aioConn) bufferedReader() {
	defer close(ac.readerDone)
	for {
		var req readReq
		select {
		case req = <-ac.reads:
		case <-ac.stop:
			return
		}
		n, err := ac.c.Read(req.data)
		if n >= coalesceMin && err == nil {
			var m int
			m, err = ac.readMore(req.data[n:])
			n += m
		}
		ac.loop.release(req.buf)
		if n > 0 {
			ac.loop.post(completion{token: ac.token.Load(), kind: evFilled, n: int64(n)})
		}
		if err != nil {
			ac.mu.Lock()
			closing := ac.closing
			ac.mu.Unlock()
			switch {
			case errors.Is(err, io.EOF):
				ac.loop.post(completion{token: ac.token.Load(), kind: evEOF})
			case closing || ac.aborted.Load():
			default:
				errno, msg := errnoOf(err)
				ac.loop.post(completion{token: ac.token.Load(), kind: evError, n: errno, msg: msg})
			}
			return
		}
	}
}

// ---- readiness selector (GoSelector) ----

// ---- module ----

type fn = func(a []*py.Object) any

// module returns the _goaio module's attributes.
func module() map[string]any {
	attrs := map[string]any{"SOURCES": sources}
	channelModule(attrs)
	def := func(name string, f fn) {
		attrs[name] = py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) { return f(a), nil })
	}
	loopOf := func(o *py.Object) *aioLoop { return lookup[*aioLoop](intArg([]*py.Object{o}, 0)) }
	connOf := func(o *py.Object) *aioConn { return lookup[*aioConn](intArg([]*py.Object{o}, 0)) }

	// Proactor loop.
	def("new_loop", func([]*py.Object) any {
		return register(&aioLoop{batch: newBatch[completion]()})
	})
	def("close_loop", func(a []*py.Object) any {
		if l := loopOf(a[0]); l != nil {
			l.closeChannelQueue()
			l.closeDone()
		}
		unregister(intArg(a, 0))
		return nil
	})
	def("wakeup", func(a []*py.Object) any {
		if l := loopOf(a[0]); l != nil {
			kick(l.wake)
		}
		return nil
	})
	def("poll", func(a []*py.Object) any {
		l := loopOf(a[0])
		for _, e := range l.handed {
			kick(e.w.rearm[e.dir]) // their callbacks ran: re-check their level
		}
		l.handed = l.handed[:0]
		evs := l.wait(timeoutArg(a[1]))
		l.closeDone()
		out := make([]any, 0, len(evs))
		var pools []*[]byte
		var helds []chan struct{}
		for _, c := range evs {
			var x, y any
			switch c.kind {
			case evReady:
				e := c.val.(selEvent)
				if e.w.closed.Load() { // unregistered (on this thread) since it was posted
					continue
				}
				l.handed = append(l.handed, e)
				x, y = e.w.fd, 1<<e.dir
			case evData:
				x, y = 0, c.data // copied into a new bytes object
				pools = append(pools, c.pool)
				helds = append(helds, c.held)
			case evError:
				x, y = c.n, c.msg
			case evGo:
				x = c.val
				if r, ok := c.val.(channelResult); ok {
					x = r.python()
					if o, ok := x.(*py.Object); ok {
						defer o.Close()
					}
				}
			case evResolved:
				x = c.addrs
			default:
				x = c.n
			}
			out = append(out, py.Tuple{c.token, c.kind, x, y})
		}
		res, err := py.From(out)
		for _, p := range pools {
			readBufs.Put(p)
		}
		for _, h := range helds { // copied out: those readers may read again
			<-h
		}
		if err != nil {
			panic(err)
		}
		return res
	})
	def("dial", func(a []*py.Object) any {
		l, token, host, port := loopOf(a[0]), intArg(a, 1), strArg(a, 2), intArg(a, 3)
		go func() {
			c, err := net.Dial("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
			if err != nil {
				errno, msg := errnoOf(err)
				l.post(completion{token: token, kind: evError, n: errno, msg: msg})
				return
			}
			l.post(completion{token: token, kind: evConnected, n: newConn(l, c)})
		}()
		return nil
	})
	tlsModule(def, loopOf, connOf)
	// pipe(loop, fd) -> conn: fd (a duplicate, which Go now owns) as a connection.
	def("pipe", func(a []*py.Object) any {
		h, err := newPipe(loopOf(a[0]), int(intArg(a, 1)))
		if err != nil {
			return oserror(err)
		}
		return h
	})
	// watch_hangup(conn): evEOF when the reading end of a write pipe closes.
	def("watch_hangup", func(a []*py.Object) any {
		if ac := connOf(a[0]); ac != nil {
			ac.watchHangup()
		}
		return nil
	})
	// wait_pid(loop, token, pid): evExited with the exit code when pid exits.
	def("wait_pid", func(a []*py.Object) any {
		waitChild(loopOf(a[0]), intArg(a, 1), intArg(a, 2))
		return nil
	})
	def("resolve", func(a []*py.Object) any {
		l, token, host := loopOf(a[0]), intArg(a, 1), strArg(a, 2)
		go func() {
			addrs, err := net.DefaultResolver.LookupHost(context.Background(), host)
			if err != nil {
				errno, msg := errnoOf(err)
				l.post(completion{token: token, kind: evError, n: errno, msg: msg})
				return
			}
			l.post(completion{token: token, kind: evResolved, addrs: addrs})
		}()
		return nil
	})
	def("listen", func(a []*py.Object) any {
		l, token, host, port := loopOf(a[0]), intArg(a, 1), strArg(a, 2), intArg(a, 3)
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
		if err != nil {
			return oserror(err)
		}
		h := register(ln)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					if errors.Is(err, net.ErrClosed) {
						return
					}
					continue
				}
				l.post(completion{token: token, kind: evAccepted, n: newConn(l, c)})
			}
		}()
		return py.Tuple{h, ln.Addr().(*net.TCPAddr).Port}
	})
	def("close_listener", func(a []*py.Object) any {
		if ln := lookup[net.Listener](intArg(a, 0)); ln != nil {
			ln.Close()
			unregister(intArg(a, 0))
		}
		return nil
	})
	// A closed connection's handle is unregistered, and a transport may still be called
	// after close() (StreamReader resumes reading once its buffer drains): these are
	// no-ops then, as on asyncio's own transports.
	def("peername", func(a []*py.Object) any {
		if ac := connOf(a[0]); ac != nil {
			return addrTuple(ac.c.RemoteAddr())
		}
		return nil
	})
	def("sockname", func(a []*py.Object) any {
		if ac := connOf(a[0]); ac != nil {
			return addrTuple(ac.c.LocalAddr())
		}
		return nil
	})
	def("set_low_water", func(a []*py.Object) any {
		ac := connOf(a[0])
		if ac == nil {
			return nil
		}
		ac.token.Store(intArg(a, 1))
		ac.mu.Lock()
		ac.low = intArg(a, 2)
		ac.mu.Unlock()
		return nil
	})
	def("start_reading", func(a []*py.Object) any {
		ac := connOf(a[0])
		if ac != nil && !ac.readerOn.Swap(true) {
			ac.token.Store(intArg(a, 1))
			go ac.reader()
		}
		return nil
	})
	def("start_reading_into", func(a []*py.Object) any {
		ac := connOf(a[0])
		if ac != nil && !ac.readerOn.Swap(true) {
			ac.token.Store(intArg(a, 1))
			go ac.bufferedReader()
		}
		return nil
	})
	// read_into(conn, buffer) exports a writable buffer and queues one read into it.
	attrs["read_into"] = py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
		ac := connOf(a[0])
		if ac == nil {
			return nil, errors.New("goaio: read_into on a closed connection")
		}
		// The loop thread closed the previous read's export before buffer_updated, so the
		// connection's one Buffer is free to export into again.
		b := &ac.rbuf
		if err := a[1].BufferInto(b, true); err != nil {
			return nil, err
		}
		data, err := b.Bytes()
		if err != nil || len(data) == 0 {
			b.Close()
			if err == nil {
				err = errors.New("get_buffer() returned an empty buffer")
			}
			return nil, err
		}
		// Try the read here first: data that is already waiting needs no trip through the
		// reader goroutine and back (two thread wakeups). The callback never waits, so
		// this is one non-blocking read(2).
		if ac.rc != nil {
			if n, ok := tryRead(ac.rc, data); ok { // n bytes, or 0 at end of stream
				b.Close()
				return n, nil
			}
		}
		select {
		case ac.reads <- readReq{b, data}:
		default:
			b.Close()
			return nil, errors.New("goaio: a read is already pending")
		}
		return nil, nil // pending: evFilled or evEOF follows
	})
	def("pause_reading", func(a []*py.Object) any {
		if ac := connOf(a[0]); ac != nil {
			ac.paused.Store(true)
		}
		return nil
	})
	def("resume_reading", func(a []*py.Object) any {
		ac := connOf(a[0])
		if ac == nil {
			return nil
		}
		ac.paused.Store(false)
		kick(ac.resume)
		return nil
	})
	def("write", func(a []*py.Object) any {
		ac := connOf(a[0])
		if ac == nil {
			return 0
		}
		data, err := a[1].UnsafeBytes()
		if err != nil {
			panic(err)
		}
		obj := a[1].NewRef() // the writer sends from the immutable bytes object
		ac.mu.Lock()
		ac.objs = append(ac.objs, obj)
		ac.queue = append(ac.queue, data)
		ac.queued += int64(len(data))
		q := ac.queued
		ac.mu.Unlock()
		kick(ac.wake)
		return q
	})
	def("write_eof", func(a []*py.Object) any {
		ac := connOf(a[0])
		if ac == nil {
			return nil
		}
		ac.mu.Lock()
		ac.eofWanted = true
		ac.mu.Unlock()
		kick(ac.wake)
		return nil
	})
	def("close", func(a []*py.Object) any {
		ac := connOf(a[0])
		if ac == nil {
			return nil
		}
		ac.mu.Lock()
		ac.closing = true
		ac.mu.Unlock()
		kick(ac.wake)
		unregister(intArg(a, 0))
		return nil
	})
	def("abort", func(a []*py.Object) any {
		ac := connOf(a[0])
		if ac == nil {
			return nil
		}
		ac.aborted.Store(true)
		ac.c.Close()
		kick(ac.wake)
		kick(ac.resume)
		unregister(intArg(a, 0))
		return nil
	})

	selectorModule(def)
	return attrs
}

// Loop is a GoEventLoop as Go code sees it, for packages that drive Python callbacks from
// goroutines (asgi).
type Loop struct{ l *aioLoop }

// LookupLoop returns the loop whose GoEventLoop._id is id, or nil once it is closed.
func LookupLoop(id int64) *Loop {
	if l := lookup[*aioLoop](id); l != nil {
		return &Loop{l}
	}
	return nil
}

// Post delivers v, converted with py.From on the loop thread, to the callback the loop
// registered for token: handler(goaio.EV_GO, v, None). Goroutines may call it at any time.
func (l *Loop) Post(token int64, v any) {
	l.l.post(completion{token: token, kind: evGo, val: v})
}

// Release closes c (a *py.Object or *py.Buffer, which need a thread state) on the loop
// thread, at its next poll.
func (l *Loop) Release(c interface{ Close() }) { l.l.release(c) }

// Wake makes the loop thread poll again soon, as call_soon_threadsafe does.
func (l *Loop) Wake() { kick(l.l.wake) }

// Hooks let Python code that belongs to a loop run on other threads while the loop
// thread waits for events, never at the same time as the loop thread (asgi's inline
// requests). The loop thread holds them while it runs Python: it calls Serve at every
// poll, attached, to run work queued for whoever holds them; Unlock before it waits,
// attached; and Lock after it waits, with its thread state released, before it returns
// to Python. Lock may block until another thread lets go.
type Hooks interface {
	Serve()
	Unlock()
	Lock()
}

// SetHooks installs h. It must be called on the loop thread (from a callback), before
// the loop next polls.
func (l *Loop) SetHooks(h Hooks) { l.l.hooks = h }
