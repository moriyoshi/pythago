package aio

import (
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/moriyoshi/pythago/py"
)

// The readiness selector (GoSelector) on Windows. asyncio's selectors there take sockets
// only, and a socket cannot join Go's poller through a duplicate as on POSIX, so each
// watched direction is a goroutine that waits in WSAPoll on the socket itself, in slices
// that let unregistering stop it, and holds an OS thread meanwhile. As on POSIX, after a
// report a watcher waits to be re-armed (the next select call), then checks the level
// again.

var wsaPoll = syscall.NewLazyDLL("ws2_32.dll").NewProc("WSAPoll")

const (
	pollRdNorm, pollRdBand, pollWrNorm = 0x0100, 0x0200, 0x0010
	pollErr, pollHup, pollNval         = 0x0001, 0x0002, 0x0004
	// pollSlice bounds how long a watcher takes to notice that it was unregistered.
	pollSlice = 50 // ms
)

type wsaPollFD struct {
	fd              uintptr
	events, revents int16
}

type selEvent struct {
	w   *selFD
	dir int
}

type selector struct {
	*batch[selEvent]
	loop   *aioLoop // set for a loop's readers and writers (notify)
	mu     sync.Mutex
	fds    map[int]*selFD
	handed []selEvent
}

type selFD struct {
	s       *selector
	fd      int
	want    atomic.Int32
	kick    [2]chan struct{} // want changed, or closed
	rearm   [2]chan struct{} // a reported direction was handled (poll), or closed
	started [2]atomic.Bool
	closed  atomic.Bool
}

// ready waits up to timeout ms for the socket to be ready in direction dir. Errors and
// hangups count as ready, as in selectors; so does a socket that is no longer valid, which
// Python then sees for itself.
func (w *selFD) ready(dir int, timeout int32) bool {
	events := int16(pollRdNorm | pollRdBand)
	if dir == 1 {
		events = pollWrNorm
	}
	pfd := wsaPollFD{fd: uintptr(w.fd), events: events}
	n, _, _ := wsaPoll.Call(uintptr(unsafe.Pointer(&pfd)), 1, uintptr(timeout))
	return int32(n) > 0 && pfd.revents&(events|pollErr|pollHup|pollNval) != 0
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
		if !w.ready(dir, pollSlice) || w.closed.Load() || w.want.Load()&bit == 0 {
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
}

// selectorModule adds the selector's functions (sel_*) to the _goaio module.
func selectorModule(def func(string, fn)) {
	selOf := func(o *py.Object) *selector { return lookup[*selector](intArg([]*py.Object{o}, 0)) }

	def("sel_new", newSelector)
	def("sel_register", func(a []*py.Object) any {
		s, fd, mask := selOf(a[0]), int(intArg(a, 1)), int32(intArg(a, 2))
		w := &selFD{s: s, fd: fd}
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
