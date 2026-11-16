package aio

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// waitHangup waits for the reading end of a write pipe to close: the netpoller reports
// the pipe's POLLERR/POLLHUP as read readiness, and poll with no events says which it was.
func waitHangup(_ *aioConn, rc syscall.RawConn) bool {
	hungUp := false
	err := rc.Read(func(fd uintptr) bool {
		fds := []unix.PollFd{{Fd: int32(fd)}} // no events: only POLLERR/POLLHUP report
		for {
			n, err := unix.Poll(fds, 0)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			hungUp = err == nil && n > 0 && fds[0].Revents&(unix.POLLERR|unix.POLLHUP) != 0
			return hungUp
		}
	})
	return err == nil && hungUp
}
