// Package pyapi resolves C API addresses from the generated pythago_symtab for the spikes.
package pyapi

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

type symtabEntry struct {
	name *byte
	addr uintptr
}

// Symbols maps every exported Py*/_Py* function and data object to its address.
var Symbols map[string]uintptr

// Load reads the NULL-terminated pythago_symtab located at base.
func Load(base uintptr) {
	Symbols = map[string]uintptr{}
	for e := (*symtabEntry)(unsafe.Pointer(base)); e.name != nil; e = (*symtabEntry)(unsafe.Add(unsafe.Pointer(e), unsafe.Sizeof(*e))) {
		Symbols[cString(e.name)] = e.addr
	}
}

// Addr returns the address of a C API symbol.
func Addr(name string) uintptr {
	a, ok := Symbols[name]
	if !ok {
		panic(fmt.Sprintf("pyapi: %s not in pythago_symtab", name))
	}
	return a
}

// Register binds fptr (a pointer to a Go func variable) to the named C function.
func Register(fptr any, name string) {
	purego.RegisterFunc(fptr, Addr(name))
}

// Pointer reads a PyObject* stored in a data symbol such as PyExc_RuntimeError.
func Pointer(name string) uintptr {
	return *(*uintptr)(unsafe.Pointer(Addr(name)))
}

func cString(p *byte) string {
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}
