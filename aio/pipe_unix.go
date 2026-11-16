//go:build !windows

package aio

import (
	"errors"
	"os"
	"syscall"
)

// newPipe takes ownership of fd (the caller duplicated it) and makes it a connection of l.
func newPipe(l *aioLoop, fd int) (int64, error) {
	if err := syscall.SetNonblock(fd, true); err != nil { // pollable: os.NewFile checks
		syscall.Close(fd)
		return 0, err
	}
	return newConn(l, pipeConn{os.NewFile(uintptr(fd), "pipe")}), nil
}

// waitChild reaps pid (wait4) and posts its exit code to token, as os.waitstatus_to_exitcode
// does: the exit status, or minus the signal number. A child someone else reaped already
// reports 255, as asyncio's own child watchers do.
func waitChild(l *aioLoop, token, pid int64) {
	go func() {
		var ws syscall.WaitStatus
		var err error
		for {
			_, err = syscall.Wait4(int(pid), &ws, 0, nil)
			if !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		code := int64(255)
		switch {
		case err != nil:
		case ws.Exited():
			code = int64(ws.ExitStatus())
		case ws.Signaled():
			code = -int64(ws.Signal())
		}
		l.post(completion{token: token, kind: evExited, n: code})
	}()
}
