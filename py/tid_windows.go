package py

import "syscall"

var getCurrentThreadID = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId")

// osThreadID identifies the calling OS thread.
func osThreadID() int64 {
	id, _, _ := getCurrentThreadID.Call()
	return int64(id)
}
