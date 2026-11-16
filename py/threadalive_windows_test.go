package py_test

import (
	"os"
	"syscall"
	"unsafe"
)

// threadAlive reports whether this process still has the OS thread tid ("yes"/"no"),
// from a Toolhelp thread snapshot.
func threadAlive(tid int) string {
	const th32csSnapThread = 0x4
	snap, _, _ := createToolhelp32Snapshot.Call(th32csSnapThread, 0)
	if syscall.Handle(snap) == syscall.InvalidHandle {
		return "unknown"
	}
	defer syscall.CloseHandle(syscall.Handle(snap))
	var entry struct { // THREADENTRY32
		size, usage, threadID, ownerProcessID uint32
		basePri, deltaPri                     int32
		flags                                 uint32
	}
	entry.size = uint32(unsafe.Sizeof(entry))
	pid := uint32(os.Getpid())
	for ok, _, _ := thread32First.Call(snap, uintptr(unsafe.Pointer(&entry))); ok != 0; ok, _, _ = thread32Next.Call(snap, uintptr(unsafe.Pointer(&entry))) {
		if entry.ownerProcessID == pid && int(entry.threadID) == tid {
			return "yes"
		}
	}
	return "no"
}
