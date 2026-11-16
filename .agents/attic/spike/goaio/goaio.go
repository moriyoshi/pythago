// Package goaio provides the _goaio extension module and goaio.py: asyncio event loops
// driven by Go.
//
//   - GoEventLoop (proactor): goroutines do the I/O with Go's net package (dialing and DNS
//     included) and queue completions for the loop.
//   - GoSelector (readiness): a selectors implementation whose registrations are goroutines
//     parked on Go's netpoller through syscall.RawConn, for asyncio.SelectorEventLoop.
//
// Neither uses a Go channel to reach Python. Work is queued in a mutex-protected batch, and
// the loop sleeps on a C event (glue pythago_vtev: a sequence counter, a parked-VT slot
// and a futex), which goroutines bump on an empty→non-empty transition. Waiting is a C
// builtin, so a loop running on a virtual thread parks the VT instead of pinning its carrier.
package goaio

import (
	"context"
	_ "embed"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/moriyoshi/pythago/internal/spike/pyapi"
)

//go:embed goaio.py
var source string

var (
	pyLongFromLongLong     func(int64) uintptr
	pyLongAsLongLong       func(uintptr) int64
	pyUnicodeAsUTF8AndSize func(uintptr, *int) *byte
	pyUnicodeFromStrSize   func(*byte, int) uintptr
	pyBytesFromStrSize     func(*byte, int) uintptr
	pyBytesAsString        func(uintptr) *byte
	pyBytesSize            func(uintptr) int
	pyTupleNew             func(int) uintptr
	pyTupleSetItem         func(uintptr, int, uintptr) int32
	pyListNew              func(int) uintptr
	pyListSetItem          func(uintptr, int, uintptr) int32
	pyCMethodNew           func(def, self, module, cls uintptr) uintptr
	pyModuleNew            func(string) uintptr
	pyObjectSetAttrStr     func(uintptr, string, uintptr) int32
	pyImportGetModuleDict  func() uintptr
	pyDictSetItemString    func(uintptr, string, uintptr) int32
	pyCompileString        func(src, filename string, start int32) uintptr
	pyImportExecCodeModule func(string, uintptr) uintptr
	pyErrSetString         func(uintptr, string)
	pyErrPrint             func()
	pyIncRef               func(uintptr)
	pyDecRef               func(uintptr)
	pyMemRawCalloc         func(n, size uintptr) uintptr
	vtevNew                func() uintptr
	vtevSet                func(uintptr)
)

// ---- builtins ----

type goFunc func(args []uintptr) uintptr

var (
	initOnce sync.Once
	funcs    []goFunc
	meth     uintptr // ml_meth for Go builtins
)

func dispatch(self, args uintptr, nargs int, kwnames uintptr) uintptr {
	f := funcs[pyLongAsLongLong(self)]
	return f(unsafe.Slice((*uintptr)(unsafe.Pointer(args)), nargs))
}

// Init registers the C API functions and the builtin dispatcher. On builds with virtual
// threads, builtins go through the glue trampoline that runs Go on the carrier stack.
func Init() {
	initOnce.Do(func() {
		for _, f := range []struct {
			p    any
			name string
		}{
			{&pyLongFromLongLong, "PyLong_FromLongLong"}, {&pyLongAsLongLong, "PyLong_AsLongLong"},
			{&pyUnicodeAsUTF8AndSize, "PyUnicode_AsUTF8AndSize"},
			{&pyUnicodeFromStrSize, "PyUnicode_FromStringAndSize"}, {&pyBytesFromStrSize, "PyBytes_FromStringAndSize"},
			{&pyBytesAsString, "PyBytes_AsString"}, {&pyBytesSize, "PyBytes_Size"},
			{&pyTupleNew, "PyTuple_New"}, {&pyTupleSetItem, "PyTuple_SetItem"},
			{&pyListNew, "PyList_New"}, {&pyListSetItem, "PyList_SetItem"},
			{&pyCMethodNew, "PyCMethod_New"}, {&pyModuleNew, "PyModule_New"},
			{&pyObjectSetAttrStr, "PyObject_SetAttrString"}, {&pyImportGetModuleDict, "PyImport_GetModuleDict"},
			{&pyDictSetItemString, "PyDict_SetItemString"}, {&pyCompileString, "Py_CompileString"},
			{&pyImportExecCodeModule, "PyImport_ExecCodeModule"},
			{&pyErrSetString, "PyErr_SetString"}, {&pyErrPrint, "PyErr_Print"},
			{&pyIncRef, "Py_IncRef"}, {&pyDecRef, "Py_DecRef"}, {&pyMemRawCalloc, "PyMem_RawCalloc"},
			{&vtevNew, "pythago_vtev_new"}, {&vtevSet, "pythago_vtev_set"},
		} {
			pyapi.Register(f.p, f.name)
		}
		cb := purego.NewCallback(dispatch)
		meth = cb
		if tramp, ok := pyapi.Symbols["pythago_vt_fastcall"]; ok {
			var setDispatch func(uintptr)
			pyapi.Register(&setDispatch, "pythago_vt_set_go_dispatch")
			setDispatch(cb)
			meth = tramp
		}
	})
}

// methodDef allocates a PyMethodDef (kept for the process lifetime).
func methodDef(name string, fn uintptr, flags uintptr) uintptr {
	cname := pyMemRawCalloc(uintptr(len(name)+1), 1)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(cname)), len(name)), name)
	md := pyMemRawCalloc(1, 32)
	d := (*[4]uintptr)(unsafe.Pointer(md))
	d[0], d[1], d[2] = cname, fn, flags
	return md
}

// Builtin returns a new reference to a Python function that calls f.
func Builtin(name string, f func(args []uintptr) uintptr) uintptr {
	Init()
	funcs = append(funcs, f)
	self := pyLongFromLongLong(int64(len(funcs) - 1))
	fn := pyCMethodNew(methodDef(name, meth, 0x82 /* METH_FASTCALL|METH_KEYWORDS */), self, 0, 0)
	pyDecRef(self)
	return fn
}

// CBuiltin returns a new reference to a Python function backed by the glue's C
// METH_FASTCALL function symbol.
func CBuiltin(name, symbol string) uintptr {
	Init()
	return cBuiltin(name, symbol)
}

// cBuiltin wraps a C METH_FASTCALL function from the glue.
func cBuiltin(name, symbol string) uintptr {
	return pyCMethodNew(methodDef(name, pyapi.Addr(symbol), 0x80 /* METH_FASTCALL */), 0, 0, 0)
}

// ---- Python object helpers ----

func none() uintptr {
	n := pyapi.Addr("_Py_NoneStruct")
	pyIncRef(n)
	return n
}

func str(o uintptr) string {
	var n int
	p := pyUnicodeAsUTF8AndSize(o, &n)
	if p == nil {
		return ""
	}
	return string(unsafe.Slice(p, n))
}

func pyStr(s string) uintptr {
	if s == "" {
		return pyUnicodeFromStrSize(nil, 0)
	}
	b := []byte(s)
	return pyUnicodeFromStrSize(&b[0], len(b))
}

func pyBytes(b []byte) uintptr {
	if len(b) == 0 {
		return pyBytesFromStrSize(nil, 0)
	}
	return pyBytesFromStrSize(&b[0], len(b))
}

func pyInt(v int64) uintptr { return pyLongFromLongLong(v) }

func tuple(items ...uintptr) uintptr {
	t := pyTupleNew(len(items))
	for i, it := range items {
		pyTupleSetItem(t, i, it) // steals
	}
	return t
}

func addrTuple(a net.Addr) uintptr {
	if ta, ok := a.(*net.TCPAddr); ok {
		return tuple(pyStr(ta.IP.String()), pyInt(int64(ta.Port)))
	}
	return none()
}

func errnoOf(err error) (int64, string) {
	var en syscall.Errno
	if errors.As(err, &en) {
		return int64(en), en.Error()
	}
	return int64(syscall.EIO), err.Error()
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

// batch is a mutex-protected queue drained by the loop thread. Producers bump the loop's
// C event only when the batch goes from empty to non-empty, and block (backpressure)
// above maxBatch until the loop drains.
type batch[T any] struct {
	ev    uintptr
	mu    sync.Mutex
	cond  *sync.Cond
	items []T
}

const maxBatch = 1 << 16

func newBatch[T any]() *batch[T] {
	b := &batch[T]{ev: vtevNew()}
	b.cond = sync.NewCond(&b.mu)
	return b
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
		vtevSet(b.ev)
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
)

type completion struct {
	token int64
	kind  int
	n     int64
	data  []byte
	pool  *[]byte
	msg   string
	addrs []string
}

type aioLoop struct {
	*batch[completion]
	decMu   sync.Mutex
	decrefs []uintptr // written bytes objects, released on the loop thread
}

func (l *aioLoop) deferDecref(o uintptr) {
	l.decMu.Lock()
	l.decrefs = append(l.decrefs, o)
	l.decMu.Unlock()
}

func (l *aioLoop) flushDecrefs() {
	l.decMu.Lock()
	ds := l.decrefs
	l.decrefs = nil
	l.decMu.Unlock()
	for _, o := range ds {
		pyDecRef(o)
	}
}

var readBufs = sync.Pool{New: func() any { b := make([]byte, 256<<10); return &b }}

type aioConn struct {
	loop       *aioLoop
	c          net.Conn
	token      atomic.Int64
	mu         sync.Mutex
	queue      [][]byte
	objs       []uintptr
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
}

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func newConn(l *aioLoop, c net.Conn) int64 {
	ac := &aioConn{loop: l, c: c, wake: make(chan struct{}, 1), resume: make(chan struct{}, 1),
		readerDone: make(chan struct{})}
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
			ac.mu.Unlock()
			break
		}
		if len(ac.queue) > 0 {
			bufs := net.Buffers(ac.queue)
			objs := ac.objs
			ac.queue, ac.objs = nil, nil
			ac.mu.Unlock()
			n, err := bufs.WriteTo(ac.c) // writev, straight from the bytes objects
			for _, o := range objs {
				ac.loop.deferDecref(o)
			}
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
			if tc, ok := ac.c.(*net.TCPConn); ok {
				tc.CloseWrite()
			}
			continue
		}
		ac.mu.Unlock()
		break
	}
	ac.c.Close()
	if ac.readerOn.Load() {
		<-ac.readerDone
	}
	ac.loop.post(completion{token: ac.token.Load(), kind: evClosed})
}

func (ac *aioConn) reader() {
	defer close(ac.readerDone)
	for {
		for ac.paused.Load() && !ac.aborted.Load() {
			<-ac.resume
		}
		pb := readBufs.Get().(*[]byte)
		n, err := ac.c.Read(*pb)
		if n > 0 {
			ac.loop.post(completion{token: ac.token.Load(), kind: evData, data: (*pb)[:n], pool: pb})
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

// ---- readiness selector (GoSelector) ----

const (
	eventRead  = 1 // selectors.EVENT_READ
	eventWrite = 2 // selectors.EVENT_WRITE
)

type selEvent struct {
	w   *selFD
	dir int
}

type selector struct {
	*batch[selEvent]
	mu     sync.Mutex
	fds    map[int]*selFD
	handed []selEvent // returned by the last poll; re-armed by the next one
}

// selFD watches one Python fd through a dup registered with Go's netpoller. Go's poller
// is edge-triggered and selectors are level-triggered, so after each report a watcher
// waits to be re-armed (the next select call), then re-checks the level before waiting
// for a new edge.
type selFD struct {
	s       *selector
	fd      int
	f       *os.File
	rc      syscall.RawConn
	want    atomic.Int32
	kick    [2]chan struct{}
	started [2]atomic.Bool
	closed  atomic.Bool
}

func levelReady(fd int, dir int) bool {
	var events int16 = 0x1 // POLLIN
	if dir == 1 {
		events = 0x4 // POLLOUT
	}
	pfd := struct {
		fd      int32
		events  int16
		revents int16
	}{int32(fd), events, 0}
	var ts syscall.Timespec
	n, _, _ := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1,
		uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
	// POLLERR, POLLHUP (and POLLRDHUP for reads) count as ready, as in selectors.
	return n > 0 && pfd.revents&(events|0x8|0x10|0x2000) != 0
}

func (w *selFD) start(dir int) {
	if !w.started[dir].Swap(true) {
		go w.watch(dir)
	}
}

func (w *selFD) watch(dir int) {
	bit := int32(1 << dir)
	for !w.closed.Load() {
		if w.want.Load()&bit == 0 {
			<-w.kick[dir]
			continue
		}
		ready := false
		check := func(fd uintptr) bool {
			if w.closed.Load() || w.want.Load()&bit == 0 {
				return true
			}
			ready = levelReady(int(fd), dir)
			return ready
		}
		var err error
		if dir == 0 {
			err = w.rc.Read(check) // parks on Go's netpoller until readable
		} else {
			err = w.rc.Write(check)
		}
		if err != nil || w.closed.Load() {
			return
		}
		if !ready {
			continue
		}
		w.s.post(selEvent{w, dir})
		<-w.kick[dir]
	}
}

func dupCloexec(fd int) (int, error) {
	r, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_DUPFD_CLOEXEC, 0)
	if e != 0 {
		return -1, e
	}
	return int(r), nil
}

// ---- module ----

// Install creates the _goaio module and imports goaio.py as module goaio.
func Install() {
	Init()
	mod := pyModuleNew("_goaio")
	def := func(name string, f goFunc) {
		fn := Builtin(name, f)
		pyObjectSetAttrStr(mod, name, fn)
		pyDecRef(fn)
	}
	for _, c := range [][2]string{{"ev_seq", "pythago_vtev_seq_py"}, {"ev_wait", "pythago_vtev_wait_py"}} {
		fn := cBuiltin(c[0], c[1])
		pyObjectSetAttrStr(mod, c[0], fn)
		pyDecRef(fn)
	}
	excOSError := pyapi.Pointer("PyExc_OSError")
	i64 := pyLongAsLongLong
	loopOf := func(o uintptr) *aioLoop { return lookup[*aioLoop](i64(o)) }
	connOf := func(o uintptr) *aioConn { return lookup[*aioConn](i64(o)) }
	selOf := func(o uintptr) *selector { return lookup[*selector](i64(o)) }

	// Proactor loop.
	def("new_loop", func([]uintptr) uintptr {
		l := &aioLoop{batch: newBatch[completion]()}
		return tuple(pyInt(register(l)), pyInt(int64(l.ev)))
	})
	def("close_loop", func(a []uintptr) uintptr { unregister(i64(a[0])); return none() })
	def("wakeup", func(a []uintptr) uintptr {
		if l := loopOf(a[0]); l != nil {
			vtevSet(l.ev)
		}
		return none()
	})
	def("poll", func(a []uintptr) uintptr {
		l := loopOf(a[0])
		evs := l.take()
		l.flushDecrefs()
		list := pyListNew(len(evs))
		for i, c := range evs {
			var x, y uintptr
			switch c.kind {
			case evData:
				x, y = pyInt(0), pyBytes(c.data)
				readBufs.Put(c.pool)
			case evError:
				x, y = pyInt(c.n), pyStr(c.msg)
			case evResolved:
				x = pyListNew(len(c.addrs))
				for j, s := range c.addrs {
					pyListSetItem(x, j, pyStr(s))
				}
				y = none()
			default:
				x, y = pyInt(c.n), none()
			}
			pyListSetItem(list, i, tuple(pyInt(c.token), pyInt(int64(c.kind)), x, y))
		}
		return list
	})
	def("dial", func(a []uintptr) uintptr {
		l, token, host, port := loopOf(a[0]), i64(a[1]), str(a[2]), i64(a[3])
		go func() {
			c, err := net.Dial("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
			if err != nil {
				errno, msg := errnoOf(err)
				l.post(completion{token: token, kind: evError, n: errno, msg: msg})
				return
			}
			l.post(completion{token: token, kind: evConnected, n: newConn(l, c)})
		}()
		return none()
	})
	def("resolve", func(a []uintptr) uintptr {
		l, token, host := loopOf(a[0]), i64(a[1]), str(a[2])
		go func() {
			addrs, err := net.DefaultResolver.LookupHost(context.Background(), host)
			if err != nil {
				l.post(completion{token: token, kind: evError, n: -2 /* EAI_NONAME */, msg: err.Error()})
				return
			}
			l.post(completion{token: token, kind: evResolved, addrs: addrs})
		}()
		return none()
	})
	def("listen", func(a []uintptr) uintptr {
		l, token, host, port := loopOf(a[0]), i64(a[1]), str(a[2]), i64(a[3])
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
		if err != nil {
			pyErrSetString(excOSError, err.Error())
			return 0
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
		return tuple(pyInt(h), pyInt(int64(ln.Addr().(*net.TCPAddr).Port)))
	})
	def("close_listener", func(a []uintptr) uintptr {
		if ln := lookup[net.Listener](i64(a[0])); ln != nil {
			ln.Close()
			unregister(i64(a[0]))
		}
		return none()
	})
	def("peername", func(a []uintptr) uintptr { return addrTuple(connOf(a[0]).c.RemoteAddr()) })
	def("sockname", func(a []uintptr) uintptr { return addrTuple(connOf(a[0]).c.LocalAddr()) })
	def("set_low_water", func(a []uintptr) uintptr {
		ac := connOf(a[0])
		ac.token.Store(i64(a[1]))
		ac.mu.Lock()
		ac.low = i64(a[2])
		ac.mu.Unlock()
		return none()
	})
	def("start_reading", func(a []uintptr) uintptr {
		ac := connOf(a[0])
		if ac != nil && !ac.readerOn.Swap(true) {
			ac.token.Store(i64(a[1]))
			go ac.reader()
		}
		return none()
	})
	def("pause_reading", func(a []uintptr) uintptr { connOf(a[0]).paused.Store(true); return none() })
	def("resume_reading", func(a []uintptr) uintptr {
		ac := connOf(a[0])
		ac.paused.Store(false)
		kick(ac.resume)
		return none()
	})
	def("write", func(a []uintptr) uintptr {
		ac := connOf(a[0])
		n := pyBytesSize(a[1])
		pyIncRef(a[1]) // the writer sends from the immutable bytes object
		data := unsafe.Slice(pyBytesAsString(a[1]), n)
		ac.mu.Lock()
		ac.objs = append(ac.objs, a[1])
		ac.queue = append(ac.queue, data)
		ac.queued += int64(n)
		q := ac.queued
		ac.mu.Unlock()
		kick(ac.wake)
		return pyInt(q)
	})
	def("write_eof", func(a []uintptr) uintptr {
		ac := connOf(a[0])
		ac.mu.Lock()
		ac.eofWanted = true
		ac.mu.Unlock()
		kick(ac.wake)
		return none()
	})
	def("close", func(a []uintptr) uintptr {
		ac := connOf(a[0])
		ac.mu.Lock()
		ac.closing = true
		ac.mu.Unlock()
		kick(ac.wake)
		unregister(i64(a[0]))
		return none()
	})
	def("abort", func(a []uintptr) uintptr {
		ac := connOf(a[0])
		if ac == nil {
			return none()
		}
		ac.aborted.Store(true)
		ac.c.Close()
		kick(ac.wake)
		kick(ac.resume)
		unregister(i64(a[0]))
		return none()
	})

	// Readiness selector.
	def("sel_new", func([]uintptr) uintptr {
		s := &selector{batch: newBatch[selEvent](), fds: map[int]*selFD{}}
		return tuple(pyInt(register(s)), pyInt(int64(s.ev)))
	})
	def("sel_register", func(a []uintptr) uintptr {
		s, fd, mask := selOf(a[0]), int(i64(a[1])), int32(i64(a[2]))
		dup, err := dupCloexec(fd)
		if err != nil {
			pyErrSetString(excOSError, err.Error())
			return 0
		}
		f := os.NewFile(uintptr(dup), "pyfd") // non-blocking fds join Go's netpoller
		rc, err := f.SyscallConn()
		if err != nil {
			f.Close()
			pyErrSetString(excOSError, err.Error())
			return 0
		}
		w := &selFD{s: s, fd: fd, f: f, rc: rc}
		w.kick[0], w.kick[1] = make(chan struct{}, 1), make(chan struct{}, 1)
		w.want.Store(mask)
		s.mu.Lock()
		s.fds[fd] = w
		s.mu.Unlock()
		for dir := range 2 {
			if mask&(1<<dir) != 0 {
				w.start(dir)
			}
		}
		return none()
	})
	def("sel_modify", func(a []uintptr) uintptr {
		s, fd, mask := selOf(a[0]), int(i64(a[1])), int32(i64(a[2]))
		s.mu.Lock()
		w := s.fds[fd]
		s.mu.Unlock()
		if w != nil {
			old := w.want.Swap(mask)
			for dir := range 2 {
				if mask&(1<<dir) != 0 && old&(1<<dir) == 0 {
					w.start(dir)
					kick(w.kick[dir])
				}
			}
		}
		return none()
	})
	def("sel_unregister", func(a []uintptr) uintptr {
		s, fd := selOf(a[0]), int(i64(a[1]))
		s.mu.Lock()
		w := s.fds[fd]
		delete(s.fds, fd)
		s.mu.Unlock()
		if w != nil {
			w.closed.Store(true)
			kick(w.kick[0])
			kick(w.kick[1])
			w.f.Close() // removes the dup from the netpoller and wakes a parked watcher
		}
		return none()
	})
	def("sel_poll", func(a []uintptr) uintptr {
		s := selOf(a[0])
		for _, e := range s.handed {
			kick(e.w.kick[e.dir]) // Python handled these: re-check their level
		}
		evs := s.take()
		s.handed = evs
		list := pyListNew(len(evs))
		for i, e := range evs {
			pyListSetItem(list, i, tuple(pyInt(int64(e.w.fd)), pyInt(int64(1<<e.dir))))
		}
		return list
	})
	def("sel_close", func(a []uintptr) uintptr {
		s := selOf(a[0])
		s.mu.Lock()
		ws := s.fds
		s.fds = map[int]*selFD{}
		s.mu.Unlock()
		for _, w := range ws {
			w.closed.Store(true)
			kick(w.kick[0])
			kick(w.kick[1])
			w.f.Close()
		}
		unregister(i64(a[0]))
		return none()
	})

	pyDictSetItemString(pyImportGetModuleDict(), "_goaio", mod)
	pyDecRef(mod)
	code := pyCompileString(source, "goaio.py", 257) // Py_file_input
	if code == 0 || pyImportExecCodeModule("goaio", code) == 0 {
		pyErrPrint()
		panic("goaio: cannot import goaio.py")
	}
	pyDecRef(code)
}
