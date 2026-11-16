//go:build !windows

package aio

import (
	"os"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/moriyoshi/pythago/py"
	"golang.org/x/sys/unix"
)

// The readiness selector (GoSelector) on POSIX systems: each registered fd is watched
// through a dup on Go's netpoller.

type selEvent struct {
	w   *selFD
	dir int
}

type selector struct {
	*batch[selEvent]
	loop   *aioLoop // set for a loop's readers and writers (notify)
	mu     sync.Mutex
	fds    map[int]*selFD
	handed []selEvent // returned by the last poll; re-armed by the next one
}

// selFD watches one Python fd through a dup registered with Go's netpoller. Go's poller
// is edge-triggered and selectors are level-triggered, so after each report a watcher
// waits to be re-armed (the next select call), then re-checks the level before waiting
// for a new edge.
//
// Go's netpoller takes only non-blocking fds. A blocking one (asyncio's pidfds for child
// processes, a blocking pipe) is watched by a goroutine blocked in ppoll on the dup and on
// an eventfd that close signals; it holds an OS thread while it waits.
type selFD struct {
	s       *selector
	fd      int
	f       *os.File        // the dup, in Go's netpoller (non-blocking fds)
	rc      syscall.RawConn // its raw access
	dup     int             // the dup (blocking fds)
	cancel  int             // waker read end that wakes the poll watchers (blocking fds), else -1
	wakeFD  int             // its write end (the same eventfd on Linux, a pipe on darwin)
	running sync.WaitGroup  // poll watchers, which must stop before dup and the waker close
	want    atomic.Int32
	kick    [2]chan struct{} // want changed, or closed
	rearm   [2]chan struct{} // a reported direction was handled (poll), or closed
	started [2]atomic.Bool
	closed  atomic.Bool
}

func levelReady(fd int, dir int) bool {
	var events int16 = unix.POLLIN
	if dir == 1 {
		events = unix.POLLOUT
	}
	pfd := []unix.PollFd{{Fd: int32(fd), Events: events}}
	n, _ := unix.Poll(pfd, 0)
	// POLLERR and POLLHUP (and Linux's POLLRDHUP for reads) count as ready, as in selectors.
	return n > 0 && pfd[0].Revents&(events|pollReadyExtra) != 0
}

func (w *selFD) start(dir int) {
	if !w.started[dir].Swap(true) {
		if w.cancel >= 0 {
			w.running.Add(1)
			go w.watchBlocking(dir)
		} else {
			go w.watch(dir)
		}
	}
}

// watchBlocking is watch for an fd outside Go's netpoller.
func (w *selFD) watchBlocking(dir int) {
	defer w.running.Done()
	bit := int32(1 << dir)
	events := int16(unix.POLLIN)
	if dir == 1 {
		events = unix.POLLOUT
	}
	for !w.closed.Load() {
		if w.want.Load()&bit == 0 {
			<-w.kick[dir]
			continue
		}
		pfds := []unix.PollFd{{Fd: int32(w.dup), Events: events}, {Fd: int32(w.cancel), Events: unix.POLLIN}}
		_, err := unix.Poll(pfds, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil || w.closed.Load() {
			return
		}
		if pfds[0].Revents&(events|pollReadyExtra) == 0 || w.want.Load()&bit == 0 {
			continue
		}
		w.s.notify(selEvent{w, dir})
		<-w.rearm[dir]
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
		w.s.notify(selEvent{w, dir})
		<-w.rearm[dir]
	}
}

func (w *selFD) close() {
	w.closed.Store(true)
	for dir := range 2 {
		kick(w.kick[dir])
		kick(w.rearm[dir])
	}
	if w.cancel < 0 {
		w.f.Close() // removes the dup from the netpoller and wakes a parked watcher
		return
	}
	wake(w.wakeFD)
	go func() { // the fds stay open while a watcher may still poll them
		w.running.Wait()
		syscall.Close(w.dup)
		syscall.Close(w.cancel)
		if w.wakeFD != w.cancel {
			syscall.Close(w.wakeFD)
		}
	}()
}

func dupCloexec(fd int) (int, error) {
	return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
}

// selectorModule adds the selector's functions (sel_*) to the _goaio module.
func selectorModule(def func(string, fn)) {
	selOf := func(o *py.Object) *selector { return lookup[*selector](intArg([]*py.Object{o}, 0)) }

	// Readiness selector.
	def("sel_new", newSelector)
	def("sel_register", func(a []*py.Object) any {
		s, fd, mask := selOf(a[0]), int(intArg(a, 1)), int32(intArg(a, 2))
		dup, err := dupCloexec(fd)
		if err != nil {
			return oserror(err)
		}
		w := &selFD{s: s, fd: fd, dup: dup, cancel: -1, wakeFD: -1}
		if fl, e := unix.FcntlInt(uintptr(dup), unix.F_GETFL, 0); e == nil && fl&unix.O_NONBLOCK != 0 {
			w.f = os.NewFile(uintptr(dup), "pyfd") // joins Go's netpoller
			if w.rc, err = w.f.SyscallConn(); err != nil {
				w.f.Close()
				return oserror(err)
			}
		} else {
			r, wfd, err := newWaker()
			if err != nil {
				syscall.Close(dup)
				return oserror(err)
			}
			w.cancel, w.wakeFD = r, wfd
		}
		for dir := range 2 {
			w.kick[dir], w.rearm[dir] = make(chan struct{}, 1), make(chan struct{}, 1)
		}
		w.want.Store(mask)
		s.mu.Lock()
		s.fds[fd] = w
		s.mu.Unlock()
		for dir := range 2 {
			if mask&(1<<dir) != 0 {
				w.start(dir)
			}
		}
		return nil
	})
	def("sel_modify", func(a []*py.Object) any {
		s, fd, mask := selOf(a[0]), int(intArg(a, 1)), int32(intArg(a, 2))
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
		return nil
	})
	def("sel_unregister", func(a []*py.Object) any {
		s, fd := selOf(a[0]), int(intArg(a, 1))
		s.mu.Lock()
		w := s.fds[fd]
		delete(s.fds, fd)
		s.mu.Unlock()
		if w != nil {
			w.close()
		}
		return nil
	})
	def("sel_poll", func(a []*py.Object) any {
		s := selOf(a[0])
		for _, e := range s.handed {
			kick(e.w.rearm[e.dir]) // Python handled these: re-check their level
		}
		evs := s.wait(timeoutArg(a[1]))
		s.handed = s.handed[:0]
		out := make([]any, 0, len(evs))
		for _, e := range evs {
			if e.w.closed.Load() { // unregistered (on this thread) since it was posted
				continue
			}
			s.handed = append(s.handed, e)
			out = append(out, py.Tuple{e.w.fd, 1 << e.dir})
		}
		return out
	})
	def("sel_close", func(a []*py.Object) any {
		s := selOf(a[0])
		s.mu.Lock()
		ws := s.fds
		s.fds = map[int]*selFD{}
		s.mu.Unlock()
		for _, w := range ws {
			w.close()
		}
		unregister(intArg(a, 0))
		return nil
	})
}
