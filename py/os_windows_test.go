package py_test

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	getCurrentThreadID       = kernel32.NewProc("GetCurrentThreadId")
	createToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	thread32First            = kernel32.NewProc("Thread32First")
	thread32Next             = kernel32.NewProc("Thread32Next")
)

// threadID is the calling OS thread's id, as threading.get_native_id reports it.
func threadID() int {
	id, _, _ := getCurrentThreadID.Call()
	return int(id)
}

// osThreads counts this process's OS threads, from a Toolhelp thread snapshot.
func osThreads() int {
	const th32csSnapThread = 0x4
	snap, _, _ := createToolhelp32Snapshot.Call(th32csSnapThread, 0)
	if syscall.Handle(snap) == syscall.InvalidHandle {
		return 0
	}
	defer syscall.CloseHandle(syscall.Handle(snap))
	var entry struct { // THREADENTRY32
		size, usage, threadID, ownerProcessID uint32
		basePri, deltaPri                     int32
		flags                                 uint32
	}
	entry.size = uint32(unsafe.Sizeof(entry))
	n := 0
	pid := uint32(os.Getpid())
	for ok, _, _ := thread32First.Call(snap, uintptr(unsafe.Pointer(&entry))); ok != 0; ok, _, _ = thread32Next.Call(snap, uintptr(unsafe.Pointer(&entry))) {
		if entry.ownerProcessID == pid {
			n++
		}
	}
	return n
}
