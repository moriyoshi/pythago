package workers

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// On Windows a worker cannot inherit a descriptor 3, Go has no FileListener there, and
// there is no SIGTERM to send. Instead:
//   - Spawn marks a duplicate of the listening socket inheritable and passes it, with the
//     read end of a stop pipe, through SysProcAttr.AdditionalInheritedHandles; the
//     handle values travel in handlesEnv;
//   - the worker accepts on the socket with an overlapped AcceptEx (socketListener), and
//     each accepted socket, overlapped, becomes an *os.File on Go's I/O completion port
//     (socketConn): reads, writes and deadlines without a thread per connection;
//   - the stop pipe's write end stays with the supervisor, which closes it to ask the
//     worker to stop; it also closes when the supervisor exits. The worker sees end of
//     file and closes stopRequested (NotifyContext).

const handlesEnv = "PYTHAGO_WORKER_HANDLES"

var (
	ws2                        = syscall.NewLazyDLL("ws2_32.dll")
	procClosesk                = ws2.NewProc("closesocket")
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procCreateEventW           = kernel32.NewProc("CreateEventW")
	procSetEvent               = kernel32.NewProc("SetEvent")
	procWaitForMultipleObjects = kernel32.NewProc("WaitForMultipleObjects")
	procGetOverlappedResult    = kernel32.NewProc("GetOverlappedResult")
)

// inheritedListener takes the listening socket and starts watching the stop pipe.
func inheritedListener() (net.Listener, error) {
	v := os.Getenv(handlesEnv)
	os.Unsetenv(handlesEnv)
	ls, ps, ok := strings.Cut(v, ",")
	lh, err1 := strconv.ParseUint(ls, 10, 64)
	ph, err2 := strconv.ParseUint(ps, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return nil, fmt.Errorf("bad %s %q", handlesEnv, v)
	}
	stop := os.NewFile(uintptr(ph), "workers-stop")
	go func() {
		io.Copy(io.Discard, stop) // until the supervisor closes its end, or exits
		stop.Close()
		close(stopRequested)
	}()
	return newSocketListener(syscall.Handle(lh))
}

// socketListener accepts on an inherited listening socket with AcceptEx. The listener
// cannot use a completion port: a socket takes one, and the supervisor's Go runtime
// associated the shared listening socket with its own. So each AcceptEx completes on an
// event instead (the OVERLAPPED's event with its low bit set, which keeps the completion
// off any port), which Accept waits for together with Close's event; Close cancels a
// pending AcceptEx (CancelIoEx) rather than relying on closing the socket to wake it.
type socketListener struct {
	h       syscall.Handle
	family  int
	addr    net.Addr
	closeEv syscall.Handle // manual reset, set by Close
	mu      sync.Mutex
	closed  bool
	active  sync.WaitGroup // Accept calls in progress, which Close waits for
}

func newSocketListener(h syscall.Handle) (*socketListener, error) {
	sa, err := syscall.Getsockname(h)
	if err != nil {
		return nil, err
	}
	syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, 0)
	ev, err := newEvent()
	if err != nil {
		return nil, err
	}
	family := syscall.AF_INET
	if _, ok := sa.(*syscall.SockaddrInet6); ok {
		family = syscall.AF_INET6
	}
	return &socketListener{h: h, family: family, addr: sockaddrTCP(sa), closeEv: ev}, nil
}

// newEvent returns a manual-reset event, not signaled.
func newEvent() (syscall.Handle, error) {
	h, _, errno := procCreateEventW.Call(0, 1, 0, 0)
	if h == 0 {
		return 0, errno
	}
	return syscall.Handle(h), nil
}

func (l *socketListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, net.ErrClosed
	}
	l.active.Add(1)
	l.mu.Unlock()
	defer l.active.Done()

	ns, err := syscall.Socket(l.family, syscall.SOCK_STREAM, syscall.IPPROTO_TCP) // overlapped
	if err != nil {
		return nil, l.opError(err)
	}
	syscall.SetHandleInformation(ns, syscall.HANDLE_FLAG_INHERIT, 0)
	ev, err := newEvent()
	if err != nil {
		procClosesk.Call(uintptr(ns))
		return nil, l.opError(err)
	}
	defer syscall.CloseHandle(ev)
	// On the heap, which does not move, for the life of the operation.
	ov := &syscall.Overlapped{HEvent: ev | 1}
	const addrLen = 28 + 16 // sizeof(SOCKADDR_IN6) + 16, as AcceptEx requires
	buf := new([2 * addrLen]byte)
	var n uint32
	err = syscall.AcceptEx(l.h, ns, &buf[0], 0, addrLen, addrLen, &n, ov)
	if err != nil && err != syscall.ERROR_IO_PENDING {
		procClosesk.Call(uintptr(ns))
		return nil, l.opError(err)
	}
	waits := [2]syscall.Handle{ev, l.closeEv}
	r, _, _ := procWaitForMultipleObjects.Call(2, uintptr(unsafe.Pointer(&waits[0])), 0, syscall.INFINITE)
	if r == syscall.WAIT_OBJECT_0+1 { // Close
		syscall.CancelIoEx(l.h, ov)
		syscall.WaitForSingleObject(ev, syscall.INFINITE) // cancelled, or completed meanwhile
		procClosesk.Call(uintptr(ns))
		runtime.KeepAlive(buf)
		return nil, net.ErrClosed
	}
	ok, _, errno := procGetOverlappedResult.Call(uintptr(l.h), uintptr(unsafe.Pointer(ov)), uintptr(unsafe.Pointer(&n)), 0)
	runtime.KeepAlive(buf)
	if ok == 0 {
		procClosesk.Call(uintptr(ns))
		return nil, l.opError(errno)
	}
	// Gives the accepted socket the listener's properties, which getpeername needs.
	lh := l.h
	if err := syscall.Setsockopt(ns, syscall.SOL_SOCKET, syscall.SO_UPDATE_ACCEPT_CONTEXT,
		(*byte)(unsafe.Pointer(&lh)), int32(unsafe.Sizeof(lh))); err != nil {
		procClosesk.Call(uintptr(ns))
		return nil, l.opError(err)
	}
	return newSocketConn(ns)
}

func (l *socketListener) opError(err error) error {
	return &net.OpError{Op: "accept", Net: "tcp", Addr: l.addr, Err: err}
}

// Close cancels a pending Accept, waits for it to return, and closes the socket.
func (l *socketListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	procSetEvent.Call(uintptr(l.closeEv))
	l.active.Wait()
	syscall.CloseHandle(l.closeEv)
	if r, _, errno := procClosesk.Call(uintptr(l.h)); r != 0 {
		return errno
	}
	return nil
}

func (l *socketListener) Addr() net.Addr { return l.addr }

// socketConn is an accepted socket as an *os.File, which Go serves through its I/O
// completion port because the socket is overlapped.
type socketConn struct {
	*os.File
	laddr, raddr net.Addr
}

func newSocketConn(h syscall.Handle) (net.Conn, error) {
	syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, 0)
	la, err := syscall.Getsockname(h)
	if err == nil {
		var ra syscall.Sockaddr
		if ra, err = syscall.Getpeername(h); err == nil {
			return &socketConn{File: os.NewFile(uintptr(h), "workers-conn"), laddr: sockaddrTCP(la), raddr: sockaddrTCP(ra)}, nil
		}
	}
	procClosesk.Call(uintptr(h))
	return nil, err
}

func (c *socketConn) LocalAddr() net.Addr  { return c.laddr }
func (c *socketConn) RemoteAddr() net.Addr { return c.raddr }

// CloseWrite shuts down the sending side, as *net.TCPConn's does.
func (c *socketConn) CloseWrite() error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = syscall.Shutdown(syscall.Handle(fd), syscall.SHUT_WR) }); err != nil {
		return err
	}
	return serr
}

func sockaddrTCP(sa syscall.Sockaddr) net.Addr {
	switch sa := sa.(type) {
	case *syscall.SockaddrInet4:
		return &net.TCPAddr{IP: net.IP(append([]byte(nil), sa.Addr[:]...)), Port: sa.Port}
	case *syscall.SockaddrInet6:
		a := &net.TCPAddr{IP: net.IP(append([]byte(nil), sa.Addr[:]...)), Port: sa.Port}
		if sa.ZoneId != 0 {
			a.Zone = strconv.FormatUint(uint64(sa.ZoneId), 10)
		}
		return a
	}
	return &net.TCPAddr{}
}

// listenerFile returns an inheritable duplicate of l's socket, for the workers.
func listenerFile(l net.Listener) (*os.File, error) {
	sc, ok := l.(syscall.Conn)
	if !ok {
		return nil, fmt.Errorf("workers: Spawn: %T has no file descriptor to pass on", l)
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return nil, err
	}
	var dup syscall.Handle
	cerr := rc.Control(func(fd uintptr) {
		p, _ := syscall.GetCurrentProcess()
		err = syscall.DuplicateHandle(p, syscall.Handle(fd), p, &dup, 0, true, syscall.DUPLICATE_SAME_ACCESS)
	})
	if cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(dup), "workers-listener"), nil
}

// stopper keeps each worker's stop pipe; closing its write end asks the worker to stop.
type stopper struct {
	mu    sync.Mutex
	pipes map[int]*os.File // by pid
}

func newStopper() *stopper { return &stopper{pipes: map[int]*os.File{}} }

// prepare passes the listener and a new stop pipe's read end to cmd; started is to be
// called after cmd.Start, which keeps the write end for a started worker.
func (s *stopper) prepare(cmd *exec.Cmd, lf *os.File) (started func(*os.Process, error), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	var rh syscall.Handle
	p, _ := syscall.GetCurrentProcess()
	if err := syscall.DuplicateHandle(p, syscall.Handle(r.Fd()), p, &rh, 0, true, syscall.DUPLICATE_SAME_ACCESS); err != nil {
		r.Close()
		w.Close()
		return nil, err
	}
	r.Close()
	lh := syscall.Handle(lf.Fd())
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{lh, rh}}
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d,%d", handlesEnv, lh, rh))
	return func(proc *os.Process, err error) {
		syscall.CloseHandle(rh) // the worker has its own
		if err != nil {
			w.Close()
			return
		}
		s.mu.Lock()
		s.pipes[proc.Pid] = w
		s.mu.Unlock()
	}, nil
}

// stop asks the worker p to stop.
func (s *stopper) stop(p *os.Process) { s.forget(p) }

// forget closes p's stop pipe, if it still has one.
func (s *stopper) forget(p *os.Process) {
	s.mu.Lock()
	w := s.pipes[p.Pid]
	delete(s.pipes, p.Pid)
	s.mu.Unlock()
	if w != nil {
		w.Close()
	}
}
