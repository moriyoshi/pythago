package py

import (
	"runtime"
	"syscall"
	"unsafe"
)

// Go→C calls on Windows go through syscall.SyscallN. The runtime keeps its call frame in
// the M rather than on the goroutine stack, so a Go callback during the call (which can
// grow and move that stack) does not disturb the result; elsewhere the thunk in
// ccall_$GOARCH.s finds its frame again through _cgo_topofstack, which Windows lacks
// without cgo.

// maxCArgs is the number of arguments a C call takes here, as on the other systems.
const maxCArgs = 8

// call calls the C function fn. It is uintptrescapes, as on the other systems: an
// argument written as uintptr(unsafe.Pointer(&x)) moves x to the heap for the call.
//
//go:uintptrescapes
func call(fn uintptr, args ...uintptr) uintptr {
	if len(args) > maxCArgs {
		panic("pythago: too many arguments for a C call")
	}
	r, _, _ := syscall.SyscallN(fn, args...)
	return r
}

//go:uintptrescapes
func call0(fn uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn)
	return r
}

//go:uintptrescapes
func call1(fn, a0 uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a0)
	return r
}

//go:uintptrescapes
func call2(fn, a0, a1 uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a0, a1)
	return r
}

//go:uintptrescapes
func call3(fn, a0, a1, a2 uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a0, a1, a2)
	return r
}

//go:uintptrescapes
func call4(fn, a0, a1, a2, a3 uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a0, a1, a2, a3)
	return r
}

//go:uintptrescapes
func call5(fn, a0, a1, a2, a3, a4 uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a0, a1, a2, a3, a4)
	return r
}

//go:uintptrescapes
func call6(fn, a0, a1, a2, a3, a4, a5 uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a0, a1, a2, a3, a4, a5)
	return r
}

//go:uintptrescapes
func call7(fn, a0, a1, a2, a3, a4, a5, a6 uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a0, a1, a2, a3, a4, a5, a6)
	return r
}

//go:uintptrescapes
func call8(fn, a0, a1, a2, a3, a4, a5, a6, a7 uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, a0, a1, a2, a3, a4, a5, a6, a7)
	return r
}

// callStr0 calls fn(s) and callStr1 fn(a0, s), with s as a C string.

//go:uintptrescapes
func callStr0(fn uintptr, s string) uintptr {
	p := cString(s)
	r, _, _ := syscall.SyscallN(fn, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	return r
}

//go:uintptrescapes
func callStr1(fn, a0 uintptr, s string) uintptr {
	p := cString(s)
	r, _, _ := syscall.SyscallN(fn, a0, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	return r
}

// dispatchCallback is dispatchFrame as a C function pointer. Windows accepts a
// syscall.NewCallback from any thread, Go's or not, so it replaces crosscall2.
var dispatchCallback = syscall.NewCallback(func(frame uintptr) uintptr {
	dispatchFrame(unsafe.Pointer(frame))
	return 0
})

// setCallback installs dispatchFrame as the glue's Go dispatcher.
func setCallback() { call2(cPySetCallback, 0, dispatchCallback) }
