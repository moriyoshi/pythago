package py_test

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

// threadID is the calling OS thread's id, as threading.get_native_id reports it
// (pthread_threadid_np, which is thread_selfid).
func threadID() int {
	id, _, _ := syscall.RawSyscall(syscall.SYS_THREAD_SELFID, 0, 0, 0)
	return int(id)
}

var procPidinfo func(pid int32, flavor int32, arg uint64, buf unsafe.Pointer, size int32) int32

// osThreads counts this process's OS threads: pti_threadnum of proc_pidinfo's
// PROC_PIDTASKINFO (struct proc_taskinfo: six uint64s, then int32s; pti_threadnum is the
// tenth).
func osThreads() int {
	if procPidinfo == nil {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		purego.RegisterLibFunc(&procPidinfo, lib, "proc_pidinfo")
	}
	const procPIDTaskInfo, size, threadnum = 4, 96, 6*8 + 9*4
	var info [size]byte
	if procPidinfo(int32(os.Getpid()), procPIDTaskInfo, 0, unsafe.Pointer(&info[0]), size) != size {
		return 0
	}
	return int(*(*int32)(unsafe.Pointer(&info[threadnum])))
}
