package aio

import (
	"io"
	"syscall"
	"unsafe"
)

// On Windows, Go's sockets are overlapped and a plain recv waits for data, so a read
// that must not wait asks how much is buffered first (FIONREAD) and receives no more than
// that. A pipe or anything else that is not a socket fails the ioctl and reads as not
// ready, which only forgoes the coalescing.

var ioctlsocket = syscall.NewLazyDLL("ws2_32.dll").NewProc("ioctlsocket")

const fionread = 0x4004667f

// recvNow receives what the socket fd has buffered, up to len(p), without waiting.
// ready is false when nothing is buffered (an end of stream included: the waiting read
// that follows reports it).
func recvNow(fd uintptr, p []byte) (n int, ready bool, err error) {
	var avail uint32
	if r, _, _ := ioctlsocket.Call(fd, fionread, uintptr(unsafe.Pointer(&avail))); r != 0 || avail == 0 || len(p) == 0 {
		return 0, false, nil
	}
	want := min(int(avail), len(p))
	buf := syscall.WSABuf{Len: uint32(want), Buf: &p[0]}
	var got, flags uint32
	if err := syscall.WSARecv(syscall.Handle(fd), &buf, 1, &got, &flags, nil, nil); err != nil {
		return 0, true, err
	}
	return int(got), true, nil
}

// readNow is one read that does not wait: errWouldBlock when nothing is ready, io.EOF at
// the end of the stream.
func readNow(rc syscall.RawConn, p []byte) (int, error) {
	var n int
	var ready bool
	var err error
	if rerr := rc.Read(func(fd uintptr) bool {
		n, ready, err = recvNow(fd, p)
		return true
	}); rerr != nil {
		return 0, rerr
	}
	switch {
	case !ready:
		return 0, errWouldBlock
	case err != nil:
		return 0, err
	case n == 0:
		return 0, io.EOF
	}
	return n, nil
}

// tryRead is one read into p that does not wait. ok is false when nothing is ready (or
// it failed: the reader goroutine then meets the error).
func tryRead(rc syscall.RawConn, p []byte) (n int, ok bool) {
	var ready bool
	var err error
	rc.Read(func(fd uintptr) bool {
		n, ready, err = recvNow(fd, p)
		return true
	})
	return n, ready && err == nil && n > 0
}
