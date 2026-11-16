package py

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Object is a Python object reference.
type Object struct {
	p        uintptr
	cleanup  runtime.Cleanup
	borrowed bool          // owned by the caller of a Go function; valid only during the call
	str      atomic.Uint32 // nativeStr's cached type check (strUnknown, strNative, strOther)
	// pre is the value C decoded for an argument of a Go function (see decode in the
	// glue), which the accessors read instead of calling C: it points into C's array of
	// the call's decoded values, and its payload into the object, both valid while the
	// argument is. releasedValue once the call has returned.
	pre *pyValue
}

// Objects and buffers that the garbage collector found unreachable before Close. Their
// references are released by the next goroutine that enters Python.
var (
	pendingMu      sync.Mutex
	pending        []uintptr // PyObject* to decref
	pendingBuffers []uintptr // Py_buffer* (in C memory) to release and free
	pendingN       atomic.Int32
)

func deferDecRef(p uintptr) {
	pendingMu.Lock()
	pending = append(pending, p)
	pendingN.Store(int32(len(pending) + len(pendingBuffers)))
	pendingMu.Unlock()
}

func deferRelease(view uintptr) {
	pendingMu.Lock()
	pendingBuffers = append(pendingBuffers, view)
	pendingN.Store(int32(len(pending) + len(pendingBuffers)))
	pendingMu.Unlock()
}

func drainPending() {
	drainGoBuffers()
	pendingMu.Lock()
	ps, bs := pending, pendingBuffers
	pending, pendingBuffers = nil, nil
	pendingN.Store(0)
	pendingMu.Unlock()
	if state.Load() == stateFinalized {
		return
	}
	for _, b := range bs {
		releaseBuffer(b)
	}
	for _, p := range ps {
		decRef(p)
	}
}

// own wraps a new (owned) reference; p == 0 returns nil.
func own(p uintptr) *Object {
	if p == 0 {
		return nil
	}
	o := &Object{p: p}
	o.cleanup = runtime.AddCleanup(o, deferDecRef, p)
	return o
}

func borrow(p uintptr) *Object { return &Object{p: p, borrowed: true} }

// newRef wraps a borrowed reference as an owned one.
func newRef(p uintptr) *Object {
	incRef(p)
	return own(p)
}

// Close releases the reference. Closing a nil, closed or borrowed Object does nothing.
func (o *Object) Close() {
	if o == nil || o.p == 0 || o.borrowed {
		return
	}
	o.cleanup.Stop()
	decRef(o.p)
	o.p = 0
}

// NewRef returns a new reference to the same object, which the caller must Close. Use it
// to keep an argument of a Go function past the call.
func (o *Object) NewRef() *Object {
	o.live()
	return newRef(o.p)
}

// Ptr returns the PyObject* for use with C APIs; o keeps the reference.
func (o *Object) Ptr() uintptr {
	o.live()
	return o.p
}

// live panics if o is a borrowed argument of a Func call that has returned
// (errReleased): its memory may hold another call's argument by now.
func (o *Object) live() {
	if o.pre == &releasedValue {
		panic(errReleased)
	}
}

// steal hands o's reference to C (a return value or a stealing API).
func (o *Object) steal() uintptr {
	if o.borrowed {
		incRef(o.p)
		return o.p
	}
	o.cleanup.Stop()
	p := o.p
	o.p = 0
	return p
}

// None returns Python's None.
func None() *Object { return newRef(cNone) }

// IsNone reports whether o is None.
func (o *Object) IsNone() bool {
	o.live()
	return o.p == cNone
}

// IsString reports whether o is a Python str (including subclasses), without
// requesting a UTF-8 representation or converting its contents.
func (o *Object) IsString() bool {
	o.live()
	if o.pre != nil && o.pre.tag == pvStr {
		return true
	}
	return int32(call1(cPyKind, o.p)) == kindStr
}

// Attr returns getattr(o, name).
func (o *Object) Attr(name string) (*Object, error) {
	o.live()
	return result(callStr1(cObjectGetAttrString, o.p, name))
}

// SetAttr does setattr(o, name, v); v is converted with [From].
func (o *Object) SetAttr(name string, v any) error {
	o.live()
	val, err := From(v)
	if err != nil {
		return err
	}
	defer val.Close()
	p := cString(name)
	rc := int32(call3(cObjectSetAttrString, o.p, uintptr(unsafe.Pointer(p)), val.p))
	runtime.KeepAlive(p)
	return status(rc)
}

// Item returns o[key].
func (o *Object) Item(key any) (*Object, error) {
	o.live()
	k, err := From(key)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	return result(call2(cObjectGetItem, o.p, k.p))
}

// SetItem does o[key] = v.
func (o *Object) SetItem(key, v any) error {
	o.live()
	k, err := From(key)
	if err != nil {
		return err
	}
	defer k.Close()
	val, err := From(v)
	if err != nil {
		return err
	}
	defer val.Close()
	return status(int32(call3(cObjectSetItem, o.p, k.p, val.p)))
}

// Len returns len(o).
func (o *Object) Len() (int, error) {
	o.live()
	n := int64(call1(cObjectSize, o.p))
	if n < 0 {
		return 0, fetchError()
	}
	return int(n), nil
}

// Truth returns bool(o).
func (o *Object) Truth() (bool, error) {
	o.live()
	if v := o.pre; v != nil {
		switch v.tag {
		case pvNone, pvFalse:
			return false, nil
		case pvTrue:
			return true, nil
		case pvInt:
			return v.a != 0, nil
		case pvFloat:
			return math.Float64frombits(v.a) != 0, nil // NaN is true, as in Python
		case pvStr, pvBytes:
			return v.b != 0, nil
		}
	}
	r := int32(call1(cObjectIsTrue, o.p))
	if r < 0 {
		return false, fetchError()
	}
	return r == 1, nil
}

// Str returns str(o).
func (o *Object) Str() (string, error) {
	o.live()
	s, err := result(call1(cObjectStr, o.p))
	if err != nil {
		return "", err
	}
	defer s.Close()
	return s.GoString()
}

// Repr returns repr(o).
func (o *Object) Repr() (string, error) {
	o.live()
	s, err := result(call1(cObjectRepr, o.p))
	if err != nil {
		return "", err
	}
	defer s.Close()
	return s.GoString()
}

// Call calls o(*args); each argument is converted with [From].
func (o *Object) Call(args ...any) (*Object, error) { return o.CallKw(args, nil) }

// CallKw calls o(*args, **kwargs).
func (o *Object) CallKw(args []any, kwargs map[string]any) (*Object, error) {
	o.live()
	var kw uintptr
	if len(kwargs) > 0 {
		d, err := From(kwargs)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		kw = d.p
	}
	p, err := callValues(o.p, args, kw)
	if err != nil {
		return nil, err
	}
	return result(p)
}

// CallMethod calls o.name(*args).
func (o *Object) CallMethod(name string, args ...any) (*Object, error) {
	m, err := o.Attr(name)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	return m.Call(args...)
}

// result wraps a new reference returned by C, or the pending exception if it is NULL.
func result(p uintptr) (*Object, error) {
	switch p {
	case 0:
		return nil, fetchError()
	case cNone:
		return singletons[0], nil
	case cTrue:
		return singletons[1], nil
	case cFalse:
		return singletons[2], nil
	}
	return own(p), nil
}

// singletons are the Objects result returns for None, True and False, shared. These are
// immortal in every CPython pythago supports (3.12+, PEP 683), so the new reference a C
// function returns for one needs no release, and the Object no GC cleanup; Close is a
// no-op on them (borrowed), and their values are preset (no C call to read them).
var singletons [3]*Object

func initSingletons() {
	singletons = [3]*Object{
		{p: cNone, borrowed: true, pre: &pyValue{tag: pvNone}},
		{p: cTrue, borrowed: true, pre: &pyValue{tag: pvTrue}},
		{p: cFalse, borrowed: true, pre: &pyValue{tag: pvFalse}},
	}
}

func status(rc int32) error {
	if rc < 0 {
		return fetchError()
	}
	return nil
}
