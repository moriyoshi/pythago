package aio

import "golang.org/x/sys/unix"

// newWaker returns a descriptor pair that wakes a poll: an eventfd, readable once
// written. Both ends are the same descriptor on Linux.
func newWaker() (r, w int, err error) {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	return fd, fd, err
}

// wake makes the waker's read end readable for good.
func wake(w int) {
	one := [8]byte{1}
	unix.Write(w, one[:])
}
