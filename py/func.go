package py

import (
	"errors"
	"sync"
	"unsafe"
)

// Func is a Go implementation of a Python callable. args and kw are borrowed: they are
// valid only during the call (keep one with NewRef). That covers the args slice, kw, and
// every *Object in them: later calls reuse their memory once this one has returned, so a
// Func must not keep them past its return, in a struct, a closure or another goroutine.
// Keep a NewRef or a converted value instead; keyword names are ordinary strings. A
// borrowed *Object used after its call returned panics until a later call reuses its
// memory, and then reads that call's argument; with [Config.CheckBorrows] it always
// panics. A Func takes any number of positional and keyword arguments. The
// result is converted with [From]; a returned *Object passes its reference to Python. A
// returned error is raised in Python: an *Error re-raises its exception, any other error
// raises RuntimeError, and so does a panic.
//
// The function runs on the calling Python thread's OS thread with its thread state
// attached; use [Release] around blocking work.
type Func func(args []*Object, kw Kwargs) (any, error)

// Kwargs are the keyword arguments of a call of a [Func], in the order the caller passed
// them. A Kwargs and the *Object values it returns are borrowed like the positional
// arguments: valid only until the Func returns. The names are Go strings that may be
// kept. The zero Kwargs has no arguments.
type Kwargs struct {
	names []string
	vals  []Object
}

// Len returns the number of keyword arguments.
func (k Kwargs) Len() int { return len(k.names) }

// Name returns the name of keyword argument i.
func (k Kwargs) Name(i int) string { return k.names[i] }

// Value returns the value of keyword argument i (borrowed).
func (k Kwargs) Value(i int) *Object { return &k.vals[i] }

// Get returns the value of the keyword argument name (borrowed), or nil if the caller
// did not pass it.
func (k Kwargs) Get(name string) *Object {
	for i, n := range k.names {
		if n == name {
			return &k.vals[i]
		}
	}
	return nil
}

// Each calls yield with each keyword argument's name and value (borrowed), in the
// caller's order, until yield returns false: range over kw.Each (`for name, v := range
// kw.Each`), which allocates nothing.
func (k Kwargs) Each(yield func(string, *Object) bool) {
	for i, n := range k.names {
		if !yield(n, &k.vals[i]) {
			return
		}
	}
}

// Map returns the keyword arguments as a new map. The values are still borrowed.
func (k Kwargs) Map() map[string]*Object {
	m := make(map[string]*Object, len(k.names))
	for i, n := range k.names {
		m[n] = &k.vals[i]
	}
	return m
}

var (
	funcsMu sync.RWMutex
	funcs   []Func
)

// NewFunc returns a Python builtin function named name that calls fn.
func NewFunc(name string, fn Func) (*Object, error) {
	const methFastcallKeywords = 0x80 | 0x02
	funcsMu.Lock()
	idx := len(funcs)
	funcs = append(funcs, fn)
	funcsMu.Unlock()
	// The PyMethodDef must outlive the function object; it and its name live in C memory
	// for the life of the process.
	cname := call2(cMemRawCalloc, uintptr(len(name)+1), 1)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(cname)), len(name)), name)
	def := call2(cMemRawCalloc, 1, 4*unsafe.Sizeof(uintptr(0)))
	d := (*[4]uintptr)(unsafe.Pointer(def))
	d[0], d[1], d[2] = cname, cPyFastcall, methFastcallKeywords
	self, err := From(idx)
	if err != nil {
		return nil, err
	}
	defer self.Close()
	return result(call4(cCMethodNew, def, self.p, 0, 0))
}

// callFrame is a call of a Func: pythago_py_fastcall's struct pythago_call.
type callFrame struct {
	args     uintptr // PyObject *const *: the positional, then the keyword arguments
	nargs    int
	kwnames  uintptr // tuple of the keyword names, or NULL
	nkw      int
	index    int // the Func's index in funcs
	result   uintptr
	borrowed uintptr // result is a borrowed reference that C turns into a new one
	decoded  uintptr // *pyValue: every argument (positional, then keyword), decoded in C
	names    uintptr // *pyValue: every keyword name, decoded in C
}

// dispatchFrame is the Go entry point of every Func: pythago_py_fastcall hands it a
// callFrame through crosscall2, or on Windows a syscall.NewCallback (see setCallback).
func dispatchFrame(frame unsafe.Pointer) {
	c := (*callFrame)(frame)
	c.result, c.borrowed = dispatch(c)
}

// dispatch runs the Func of call c. Its arguments, positional slice and keyword names
// come from a call scope, released once the Func has returned (or panicked) and
// returnValue has read its result, which may be one of the arguments. An argument's
// decoded value stays in C's array (pre points into it), which lives until the call
// returns, as the argument does.
func dispatch(c *callFrame) (ret, borrowed uintptr) {
	funcsMu.RLock()
	var fn Func
	if c.index < len(funcs) {
		fn = funcs[c.index]
	}
	funcsMu.RUnlock()
	if fn == nil {
		raise(errors.New("pythago: unknown Go function"))
		return 0, 0
	}
	n := c.nargs + c.nkw
	argv := unsafe.Slice((*uintptr)(unsafe.Pointer(c.args)), n)
	dec := unsafe.Slice((*pyValue)(unsafe.Pointer(c.decoded)), n)
	sc := acquireCall()
	defer func() {
		if v := recover(); v != nil {
			raise(panicError(v))
			ret, borrowed = 0, 0
		}
		sc.release()
	}()
	var (
		objs  []Object
		pos   []*Object
		names []string
	)
	if n <= len(sc.objsIn) {
		objs, pos, names = sc.objsIn[:n:n], sc.ptrsIn[:c.nargs:c.nargs], sc.strsIn[:c.nkw:c.nkw]
		sc.inline = n
	} else {
		objs, pos, names = sc.objs.Take(n), sc.ptrs.Take(c.nargs), sc.strs.Take(c.nkw)
	}
	for i := range objs {
		// The whole Object is written: a reused one keeps nothing of an earlier call
		// (the poison, the str cache).
		objs[i] = Object{p: argv[i], borrowed: true}
		if dec[i].tag != pvObject {
			objs[i].pre = &dec[i]
		}
	}
	for i := range pos {
		pos[i] = &objs[i]
	}
	for i := range names {
		var err error
		if names[i], err = kwName(c, i, &sc.n); err != nil {
			raise(err)
			return 0, 0
		}
	}
	res, err := fn(pos, Kwargs{names: names, vals: objs[c.nargs:]})
	if err != nil {
		raise(err)
		return 0, 0
	}
	ret, borrowed, err = returnValue(res)
	if err != nil {
		raise(err)
		return 0, 0
	}
	return ret, borrowed
}

// returnValue converts a Func's result for C: a new reference, or (borrowed = 1) one
// that C takes a new reference to itself, which saves a C call for None, booleans and
// returned arguments. A nil *Object returns None.
func returnValue(res any) (p, borrowed uintptr, err error) {
	switch v := res.(type) {
	case nil:
		return cNone, 1, nil
	case bool:
		if v {
			return cTrue, 1, nil
		}
		return cFalse, 1, nil
	case *Object:
		if v == nil {
			return cNone, 1, nil
		}
		if v.borrowed {
			return v.p, 1, nil
		}
		return v.steal(), 0, nil
	}
	p, err = fromRaw(res)
	return p, 0, err
}

// NewModule creates a module with the given attributes and adds it to sys.modules, so
// Python code can import it. Func values become builtin functions; other values are
// converted with [From].
func NewModule(name string, attrs map[string]any) (*Object, error) {
	m, err := result(callStr0(cModuleNew, name))
	if err != nil {
		return nil, err
	}
	for k, v := range attrs {
		if f, ok := v.(func([]*Object, Kwargs) (any, error)); ok {
			v = Func(f)
		}
		if f, ok := v.(Func); ok {
			fo, err := NewFunc(k, f)
			if err != nil {
				m.Close()
				return nil, err
			}
			v = fo
			defer fo.Close()
		}
		if err := m.SetAttr(k, v); err != nil {
			m.Close()
			return nil, err
		}
	}
	modules := borrow(call0(cImportGetModuleDict))
	if err := modules.SetItem(name, m); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}
