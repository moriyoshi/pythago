package py

import "syscall"

// osThreadID identifies the calling OS thread.
func osThreadID() int64 { return int64(syscall.Gettid()) }
