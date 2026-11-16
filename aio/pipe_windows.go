package aio

import (
	"os"
	"syscall"
)

// newPipe takes ownership of the pipe handle h (the caller duplicated it) and makes it a
// connection of l. Anonymous pipes are synchronous on Windows, so the connection's reader
// and writer goroutines each hold an OS thread while they wait.
func newPipe(l *aioLoop, h int) (int64, error) {
	return newConn(l, pipeConn{os.NewFile(uintptr(h), "pipe")}), nil
}

// waitChild waits for process pid to exit and posts its exit code to token. A process
// that cannot be opened (already gone and reaped) reports 255, as on the other systems.
func waitChild(l *aioLoop, token, pid int64) {
	go func() {
		code := int64(255)
		const synchronize, queryLimited = 0x00100000, 0x1000
		if h, err := syscall.OpenProcess(synchronize|queryLimited, false, uint32(pid)); err == nil {
			if ev, _ := syscall.WaitForSingleObject(h, syscall.INFINITE); ev == syscall.WAIT_OBJECT_0 {
				var c uint32
				if syscall.GetExitCodeProcess(h, &c) == nil {
					code = int64(int32(c))
				}
			}
			syscall.CloseHandle(h)
		}
		l.post(completion{token: token, kind: evExited, n: code})
	}()
}
