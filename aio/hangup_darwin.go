package aio

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// waitHangup waits for the reading end of a write pipe to close. On darwin poll() reports
// no POLLHUP for a pipe's write end, but kqueue's EVFILT_WRITE says EV_EOF. It gives up
// when the connection is done (ac.stop), checking twice a second.
func waitHangup(ac *aioConn, rc syscall.RawConn) bool {
	fd := -1
	rc.Control(func(f uintptr) { fd = int(f) })
	kq, err := unix.Kqueue()
	if err != nil || fd < 0 {
		return false
	}
	defer unix.Close(kq)
	ev := []unix.Kevent_t{{Ident: uint64(fd), Filter: unix.EVFILT_WRITE, Flags: unix.EV_ADD | unix.EV_CLEAR}}
	if _, err := unix.Kevent(kq, ev, nil, nil); err != nil {
		return false
	}
	out := make([]unix.Kevent_t, 1)
	ts := unix.NsecToTimespec(int64(500 * time.Millisecond))
	for {
		select {
		case <-ac.stop:
			return false
		default:
		}
		n, err := unix.Kevent(kq, nil, out, &ts)
		switch {
		case errors.Is(err, unix.EINTR):
		case err != nil:
			return false
		case n > 0 && out[0].Flags&unix.EV_EOF != 0:
			return true
		}
	}
}
