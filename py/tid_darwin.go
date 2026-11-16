package py

import "syscall"

// osThreadID identifies the calling OS thread: thread_selfid(2), the 64-bit id that
// pthread_threadid_np also reports.
func osThreadID() int64 {
	id, _, _ := syscall.RawSyscall(syscall.SYS_THREAD_SELFID, 0, 0, 0)
	return int64(id)
}
