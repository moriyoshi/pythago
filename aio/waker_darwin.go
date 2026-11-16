package aio

import "syscall"

// newWaker returns a descriptor pair that wakes a poll: a pipe, since darwin has no
// eventfd. Both ends are close-on-exec.
func newWaker() (r, w int, err error) {
	var p [2]int
	syscall.ForkLock.RLock()
	err = syscall.Pipe(p[:])
	if err == nil {
		syscall.CloseOnExec(p[0])
		syscall.CloseOnExec(p[1])
	}
	syscall.ForkLock.RUnlock()
	return p[0], p[1], err
}

// wake makes the waker's read end readable for good.
func wake(w int) { syscall.Write(w, []byte{1}) }
