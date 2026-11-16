package py_test

import (
	"os"
	"syscall"
)

// threadID is the calling OS thread's id, as threading.get_native_id reports it.
func threadID() int { return syscall.Gettid() }

// osThreads counts this process's OS threads.
func osThreads() int {
	tasks, _ := os.ReadDir("/proc/self/task")
	return len(tasks)
}
