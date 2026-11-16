//go:build !windows

package aio

import (
	"io"
	"syscall"
)

// readNow is one read(2) that does not wait: errWouldBlock when nothing is ready, io.EOF
// at the end of the stream.
func readNow(rc syscall.RawConn, p []byte) (int, error) {
	var n int
	var err error
	if rerr := rc.Read(func(fd uintptr) bool {
		for {
			n, err = syscall.Read(int(fd), p)
			if err != syscall.EINTR {
				return true
			}
		}
	}); rerr != nil {
		return 0, rerr
	}
	switch {
	case err == syscall.EAGAIN:
		return 0, errWouldBlock
	case err != nil:
		return 0, err
	case n == 0:
		return 0, io.EOF
	}
	return n, nil
}

// tryRead is one read(2) into p that does not wait. ok is false when nothing is ready (or
// it failed: the reader goroutine then meets the error); n is 0 at the end of the stream.
func tryRead(rc syscall.RawConn, p []byte) (n int, ok bool) {
	var rerr error = syscall.EAGAIN
	rc.Read(func(fd uintptr) bool {
		for {
			n, rerr = syscall.Read(int(fd), p)
			if rerr != syscall.EINTR {
				return true
			}
		}
	})
	return max(n, 0), rerr == nil
}
