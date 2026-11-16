package py

import "unsafe"

// DebugHooks reports whether CPython's memory allocators run on their debug hooks (-X dev,
// PYTHONMALLOC=debug). It probes the RAW domain, which the fusion does not wrap: with the
// hooks, a block of n bytes has its API id ('r') at -8 and a forbidden byte (0xfd) at -1
// and at n (Objects/obmalloc.c, the debug hooks' layout); with glibc's malloc, -1 is the
// top byte of the chunk's size, 0.
func DebugHooks() bool {
	const n = 8
	p := call1(symbol("PyMem_RawMalloc"), n)
	if p == 0 {
		return false
	}
	defer call1(symbol("PyMem_RawFree"), p)
	at := func(off int) byte { return *(*byte)(unsafe.Add(unsafe.Pointer(p), off)) }
	return at(-8) == 'r' && at(-1) == 0xfd && at(n) == 0xfd
}
