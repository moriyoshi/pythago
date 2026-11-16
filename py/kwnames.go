package py

import (
	"sync/atomic"
	"unsafe"
)

// kwNameCache holds Go copies of the keyword names Go functions are called with, so that
// a call with the same names as an earlier one allocates no strings. A name's set is
// chosen by the address of its UTF-8 form, which stays the same for the str a call site
// passes on every call (code objects keep their keyword names), and an entry is used only
// if its contents equal the name's: the cache holds no Python objects, so a str that
// dies and another that takes its memory cannot be confused, and it serves every
// interpreter alike. Each set holds two names, the most recently added first, so two
// names that share a set do not evict each other on every call.
var kwNameCache [256][2]atomic.Pointer[string]

// kwName returns the name of keyword argument i of the call c. n is scratch space for
// the C API's length result, which a local would make escape on every call.
func kwName(c *callFrame, i int, n *int64) (string, error) {
	var b []byte
	if v := (*pyValue)(unsafe.Add(unsafe.Pointer(c.names), uintptr(i)*unsafe.Sizeof(pyValue{}))); v.tag == pvStr {
		b = v.payload()
	} else {
		item := call2(cTupleGetItem, c.kwnames, uintptr(i))
		p := call2(cUnicodeAsUTF8AndSize, item, uintptr(unsafe.Pointer(n)))
		if p == 0 {
			return "", fetchError()
		}
		b = unsafe.Slice((*byte)(unsafe.Pointer(p)), *n)
	}
	set := &kwNameCache[uint64(uintptr(unsafe.Pointer(unsafe.SliceData(b))))*0x9e3779b97f4a7c15>>56]
	for w := range set {
		if s := set[w].Load(); s != nil && *s == string(b) {
			return *s, nil
		}
	}
	// Concurrent misses may lose an entry; a later call adds it again.
	s := string(b)
	set[1].Store(set[0].Load())
	set[0].Store(&s)
	return s, nil
}
