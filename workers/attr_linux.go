package workers

import "syscall"

// workerAttr makes the kernel send SIGTERM to a worker when the thread that started
// it exits; Spawn starts workers from one locked thread that lives as long as it does.
func workerAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

// watchParent is not needed on Linux: the parent-death signal covers it.
func watchParent() {}
