//go:build !windows

package py

import (
	"runtime"
	"unsafe"
)

// Go→C calls. Each call is one runtime.cgocall (the runtime's own Go→C transition, which
// it lets packages link to) of an assembly thunk that loads the arguments from a cframe
// and calls the C function. Compared with purego.SyscallN, this skips the pooled
// argument block, the variadic marshalling and the floating-point register shuffling,
// which halves the cost of a call.

//go:linkname runtimeCgocall runtime.cgocall
//go:noescape
func runtimeCgocall(fn uintptr, arg unsafe.Pointer) int32

// cthunk is the address of pythago_cthunk, and crosscall2 that of crosscall2
// (ccall_$GOARCH.s).
var cthunk, crosscall2 uintptr

// cframe is a C call: the function, its integer or pointer arguments, and its result. Its
// layout is read by pythago_cthunk.
type cframe struct {
	fn   uintptr
	args [maxCArgs]uintptr
	ret  uintptr
}

// maxCArgs is the number of arguments the thunk passes: all in registers on arm64; the
// last two on the stack on amd64, which has six integer argument registers.
const maxCArgs = 8

// call calls the C function fn. It is uintptrescapes: an argument written as
// uintptr(unsafe.Pointer(&x)) moves x to the heap and keeps it alive for the call.
// Without the pragma x may stay on the goroutine stack, which a Go callback during the
// call can move, leaving C writing to the old location (out-parameters read back as zero).
//
//go:uintptrescapes
func call(fn uintptr, args ...uintptr) uintptr {
	if len(args) > maxCArgs {
		panic("pythago: too many arguments for a C call")
	}
	f := cframe{fn: fn}
	copy(f.args[:], args)
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

// call0 … call8 are call for a fixed number of arguments. They are what Go code here
// uses: a variadic uintptrescapes call heap-allocates its argument array, these do not.

//go:uintptrescapes
func call0(fn uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

//go:uintptrescapes
func call1(fn, a0 uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

//go:uintptrescapes
func call2(fn, a0, a1 uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0, a1}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

//go:uintptrescapes
func call3(fn, a0, a1, a2 uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0, a1, a2}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

//go:uintptrescapes
func call4(fn, a0, a1, a2, a3 uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0, a1, a2, a3}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

//go:uintptrescapes
func call5(fn, a0, a1, a2, a3, a4 uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0, a1, a2, a3, a4}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

//go:uintptrescapes
func call6(fn, a0, a1, a2, a3, a4, a5 uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0, a1, a2, a3, a4, a5}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

//go:uintptrescapes
func call7(fn, a0, a1, a2, a3, a4, a5, a6 uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0, a1, a2, a3, a4, a5, a6}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

//go:uintptrescapes
func call8(fn, a0, a1, a2, a3, a4, a5, a6, a7 uintptr) uintptr {
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0, a1, a2, a3, a4, a5, a6, a7}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	return f.ret
}

// callStr0 calls fn(s) and callStr1 fn(a0, s), with s as a C string. Like call0 … call8
// they take a fixed number of arguments: a variadic uintptrescapes call heap-allocates
// its argument array, so the C string is the only allocation.

//go:uintptrescapes
func callStr0(fn uintptr, s string) uintptr {
	p := cString(s)
	f := cframe{fn: fn, args: [maxCArgs]uintptr{uintptr(unsafe.Pointer(p))}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	runtime.KeepAlive(p)
	return f.ret
}

//go:uintptrescapes
func callStr1(fn, a0 uintptr, s string) uintptr {
	p := cString(s)
	f := cframe{fn: fn, args: [maxCArgs]uintptr{a0, uintptr(unsafe.Pointer(p))}}
	runtimeCgocall(cthunk, unsafe.Pointer(&f))
	runtime.KeepAlive(p)
	return f.ret
}

// dispatchFrameFn holds dispatchFrame as a func value, whose first word is the
// ABIInternal entry crosscall2 expects.
var dispatchFrameFn = dispatchFrame

// setCallback installs dispatchFrame as the glue's Go dispatcher.
func setCallback() {
	entry := **(**uintptr)(unsafe.Pointer(&dispatchFrameFn))
	call2(cPySetCallback, crosscall2, entry)
}
