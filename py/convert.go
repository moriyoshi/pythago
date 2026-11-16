package py

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"runtime"
	"sync"
	"unsafe"

	"github.com/moriyoshi/pythago/internal/fusion"
	"github.com/moriyoshi/pythago/internal/lease"
)

// Tuple converts to a Python tuple; other slices convert to lists.
type Tuple []any

var channelConversion struct {
	sync.RWMutex
	convert func(any) (*Object, bool, error)
}

// RegisterChannelConverter installs the channel adapter supplied by package aio.
// Call before initializing Python. The converter runs with a thread state.
// Return handled=false for values that are not supported channels.
func RegisterChannelConverter(convert func(any) (*Object, bool, error)) {
	channelConversion.Lock()
	channelConversion.convert = convert
	channelConversion.Unlock()
}

// Object kinds reported by pythago_py_kind.
const (
	kindOther = iota
	kindNone
	kindBool
	kindInt
	kindFloat
	kindStr
	kindBytes
	kindByteArray
	kindList
	kindTuple
	kindDict
)

// From converts a Go value to a new Python object:
//
//	nil                         None
//	*Object                     the same object (a new reference)
//	bool                        bool
//	signed and unsigned ints    int
//	*big.Int                    int
//	float32, float64            float
//	string                      str
//	[]byte                      bytes
//	Tuple                       tuple
//	other slices and arrays     list
//	maps                        dict
//	Func                        a builtin function calling it
//	chan T                      asyncio channel (requires aio.Configure)
func From(v any) (*Object, error) {
	p, err := fromRaw(v)
	if err != nil {
		return nil, err
	}
	return own(p), nil
}

// rawResult returns p, a new reference, or the raised exception when p is NULL.
func rawResult(p uintptr) (uintptr, error) {
	if p == 0 {
		return 0, fetchError()
	}
	return p, nil
}

// fromRaw is From returning a new reference as a raw PyObject*. Nested values go
// straight into their container, so only the outermost object gets an Object (and a GC
// cleanup).
func fromRaw(v any) (uintptr, error) {
	switch v := v.(type) {
	case nil:
		incRef(cNone)
		return cNone, nil
	case *Object:
		incRef(v.p)
		return v.p, nil
	case bool:
		if v {
			incRef(cTrue)
			return cTrue, nil
		}
		incRef(cFalse)
		return cFalse, nil
	case int:
		return rawResult(call1(cLongFromLongLong, uintptr(v)))
	case int8:
		return rawResult(call1(cLongFromLongLong, uintptr(v)))
	case int16:
		return rawResult(call1(cLongFromLongLong, uintptr(v)))
	case int32:
		return rawResult(call1(cLongFromLongLong, uintptr(v)))
	case int64:
		return rawResult(call1(cLongFromLongLong, uintptr(v)))
	case uint:
		return rawResult(call1(cLongFromUnsignedLongLong, uintptr(v)))
	case uint8:
		return rawResult(call1(cLongFromUnsignedLongLong, uintptr(v)))
	case uint16:
		return rawResult(call1(cLongFromUnsignedLongLong, uintptr(v)))
	case uint32:
		return rawResult(call1(cLongFromUnsignedLongLong, uintptr(v)))
	case uint64:
		return rawResult(call1(cLongFromUnsignedLongLong, uintptr(v)))
	case uintptr:
		return rawResult(call1(cLongFromUnsignedLongLong, v))
	case *big.Int:
		p := cString(v.Text(10))
		r := call3(cLongFromString, uintptr(unsafe.Pointer(p)), 0, 10)
		runtime.KeepAlive(p)
		return rawResult(r)
	case float64:
		return rawResult(floatFromDouble(v))
	case float32:
		return rawResult(floatFromDouble(float64(v)))
	case string:
		r := call2(cUnicodeFromStringAndSize, uintptr(unsafe.Pointer(unsafe.StringData(v))), uintptr(len(v)))
		runtime.KeepAlive(v)
		return rawResult(r)
	case []byte:
		r := call2(cBytesFromStringAndSize, uintptr(unsafe.Pointer(unsafe.SliceData(v))), uintptr(len(v)))
		runtime.KeepAlive(v)
		return rawResult(r)
	case Tuple:
		return build(buildTuple, len(v), func(i int) any { return v[i] })
	case []any:
		return build(buildList, len(v), func(i int) any { return v[i] })
	case *fusion.Bytes:
		return fromFused(v) // built in place, no copy
	case lease.Bytes:
		// Copied, then released: the lender may reuse the memory from then on.
		b := v.LeasedBytes()
		r := call2(cBytesFromStringAndSize, uintptr(unsafe.Pointer(unsafe.SliceData(b))), uintptr(len(b)))
		runtime.KeepAlive(b)
		v.ReleaseLease()
		return rawResult(r)
	case Func:
		return funcRaw(v)
	case func([]*Object, Kwargs) (any, error):
		return funcRaw(v)
	}
	channelConversion.RLock()
	convert := channelConversion.convert
	channelConversion.RUnlock()
	if convert != nil {
		o, handled, err := convert(v)
		if err != nil {
			return 0, err
		}
		if handled {
			return o.steal(), nil
		}
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return build(buildList, rv.Len(), func(i int) any { return rv.Index(i).Interface() })
	case reflect.Map:
		kv := make([]any, 0, 2*rv.Len())
		for it := rv.MapRange(); it.Next(); {
			kv = append(kv, it.Key().Interface(), it.Value().Interface())
		}
		return build(buildDict, len(kv), func(i int) any { return kv[i] })
	}
	return 0, fmt.Errorf("py: cannot convert %T to a Python object", v)
}

func funcRaw(fn Func) (uintptr, error) {
	o, err := NewFunc("go_func", fn)
	if err != nil {
		return 0, err
	}
	return o.steal(), nil
}

// pyValue is a Python value described for or by C (glue struct pythago_value): an item
// for pythago_py_build, or an argument of a Go function decoded by pythago_py_fastcall.
type pyValue struct {
	tag uint64
	a   uint64 // the PyObject*; the integer or float bits; or the payload's arena offset
	// (build) or address (decoded)
	b uint64 // the payload's length
}

// payload is a decoded str or bytes value's contents, which the object owns.
func (v *pyValue) payload() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(v.a))), v.b)
}

const (
	pvObject = iota // build: a new reference, consumed (also on failure); decoded: not decoded
	pvNone
	pvTrue
	pvFalse
	pvInt
	pvUint
	pvFloat
	pvStr
	pvBytes
	pvBorrowed // a borrowed reference: the container takes a new one
	pvReleased // Go only: a borrowed Object whose call returned (releasedValue)
)

const (
	buildTuple = iota
	buildList
	buildDict // items are (key, value) pairs
)

// maxInline is the largest str or bytes payload copied into the arena. Larger ones are
// converted on their own, without the copy.
const maxInline = 256

// build converts item(0) … item(n-1) and builds a tuple, list or dict (of pairs) from
// them in one C call: scalars and small payloads are created in C, other values are
// converted first. The described values and the payload arena come from a call scope
// (a nested container's build takes one of its own).
func build(kind uintptr, n int, item func(int) any) (uintptr, error) {
	sc := acquireCall()
	defer sc.release()
	items := sc.vals.Take(n)
	arena := sc.bytes.Rest()
	var err error
	for i := range n {
		if err = setValue(&items[i], item(i), &arena); err != nil {
			releaseValues(items[:i])
			break
		}
	}
	sc.bytes.Adopt(arena)
	if err != nil {
		return 0, err
	}
	// Both arrays are passed in the call expression, so call4's uintptrescapes keeps
	// them on the heap: C must never read goroutine-stack memory (a call scope is heap).
	r := call4(cPyBuild, kind, uintptr(n), uintptr(unsafe.Pointer(unsafe.SliceData(items))),
		uintptr(unsafe.Pointer(unsafe.SliceData(arena))))
	runtime.KeepAlive(item) // the *Object values passed as borrowed references
	return rawResult(r)
}

// callValues calls callable with args and kwargs (a dict, or 0) in one C call: the
// arguments are described as for build, created in C and released after the call. It
// returns a new reference, 0 with the exception set, or a conversion error.
func callValues(callable uintptr, args []any, kwargs uintptr) (uintptr, error) {
	sc := acquireCall()
	defer sc.release()
	items := sc.vals.Take(len(args))
	arena := sc.bytes.Rest()
	for i, a := range args {
		if err := setValue(&items[i], a, &arena); err != nil {
			releaseValues(items[:i])
			sc.bytes.Adopt(arena)
			return 0, err
		}
	}
	sc.bytes.Adopt(arena)
	// Passed in the call expression: call5's uintptrescapes keeps both arrays on the
	// heap (C must never read goroutine-stack memory).
	r := call5(cPyCall, callable, uintptr(len(args)), uintptr(unsafe.Pointer(unsafe.SliceData(items))),
		uintptr(unsafe.Pointer(unsafe.SliceData(arena))), kwargs)
	runtime.KeepAlive(args) // the *Object values passed as borrowed references
	return r, nil
}

// setValue describes v for pythago_py_build.
func setValue(pv *pyValue, v any, arena *[]byte) error {
	switch v := v.(type) {
	case nil:
		*pv = pyValue{tag: pvNone}
	case bool:
		*pv = pyValue{tag: pvFalse}
		if v {
			pv.tag = pvTrue
		}
	case int:
		*pv = pyValue{tag: pvInt, a: uint64(v)}
	case int8:
		*pv = pyValue{tag: pvInt, a: uint64(v)}
	case int16:
		*pv = pyValue{tag: pvInt, a: uint64(v)}
	case int32:
		*pv = pyValue{tag: pvInt, a: uint64(v)}
	case int64:
		*pv = pyValue{tag: pvInt, a: uint64(v)}
	case uint:
		*pv = pyValue{tag: pvUint, a: uint64(v)}
	case uint8:
		*pv = pyValue{tag: pvUint, a: uint64(v)}
	case uint16:
		*pv = pyValue{tag: pvUint, a: uint64(v)}
	case uint32:
		*pv = pyValue{tag: pvUint, a: uint64(v)}
	case uint64:
		*pv = pyValue{tag: pvUint, a: v}
	case uintptr:
		*pv = pyValue{tag: pvUint, a: uint64(v)}
	case float64:
		*pv = pyValue{tag: pvFloat, a: math.Float64bits(v)}
	case float32:
		*pv = pyValue{tag: pvFloat, a: math.Float64bits(float64(v))}
	case string:
		if len(v) > maxInline {
			return setObject(pv, v)
		}
		*pv = pyValue{tag: pvStr, a: uint64(len(*arena)), b: uint64(len(v))}
		*arena = append(*arena, v...)
	case []byte:
		if len(v) > maxInline {
			return setObject(pv, v)
		}
		*pv = pyValue{tag: pvBytes, a: uint64(len(*arena)), b: uint64(len(v))}
		*arena = append(*arena, v...)
	case *Object:
		if v == nil || v.p == 0 {
			return setObject(pv, v)
		}
		*pv = pyValue{tag: pvBorrowed, a: uint64(v.p)}
	default:
		return setObject(pv, v)
	}
	return nil
}

// setObject converts v on its own.
func setObject(pv *pyValue, v any) error {
	p, err := fromRaw(v)
	if err != nil {
		return err
	}
	*pv = pyValue{tag: pvObject, a: uint64(p)}
	return nil
}

// releaseValues drops the references of items that were never handed to C.
func releaseValues(items []pyValue) {
	for _, pv := range items {
		if pv.tag == pvObject {
			decRef(uintptr(pv.a))
		}
	}
}

// Go converts o to a Go value: None to nil, bool, int to int64 (or *big.Int when it does
// not fit), float to float64, str to string, bytes and bytearray to []byte, list and
// tuple to []any, dict to map[any]any. Any other object is returned as a new *Object.
func (o *Object) Go() (any, error) {
	o.live()
	if v := o.pre; v != nil {
		switch v.tag {
		case pvNone:
			return nil, nil
		case pvTrue, pvFalse:
			return v.tag == pvTrue, nil
		case pvInt:
			return int64(v.a), nil
		case pvFloat:
			return math.Float64frombits(v.a), nil
		case pvStr:
			return string(v.payload()), nil
		case pvBytes:
			return append([]byte(nil), v.payload()...), nil
		}
	}
	switch int32(call1(cPyKind, o.p)) {
	case kindNone:
		return nil, nil
	case kindBool:
		return o.p == cTrue, nil
	case kindInt:
		var overflow int32
		n := int64(call2(cLongAsLongLongAndOverflow, o.p, uintptr(unsafe.Pointer(&overflow))))
		if overflow == 0 {
			if n == -1 && call0(cErrOccurred) != 0 {
				return nil, fetchError()
			}
			return n, nil
		}
		s, err := o.Str()
		if err != nil {
			return nil, err
		}
		b, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return nil, fmt.Errorf("py: cannot parse int %q", s)
		}
		return b, nil
	case kindFloat:
		return o.Float64()
	case kindStr:
		return o.GoString()
	case kindBytes, kindByteArray:
		return o.Bytes()
	case kindList:
		n := int(int64(call1(cListSize, o.p)))
		out := make([]any, 0, n)
		for i := range n {
			it := call2(cListGetItemRef, o.p, uintptr(i)) // a new reference, released below
			if it == 0 {
				return nil, fetchError()
			}
			v, err := borrow(it).Go()
			decRef(it)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case kindTuple:
		n := int(int64(call1(cTupleSize, o.p)))
		out := make([]any, n)
		for i := range n {
			v, err := borrow(call2(cTupleGetItem, o.p, uintptr(i))).Go()
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case kindDict:
		items := call1(cDictItems, o.p)
		if items == 0 {
			return nil, fetchError()
		}
		kvs, err := borrow(items).Go()
		decRef(items)
		if err != nil {
			return nil, err
		}
		out := map[any]any{}
		for _, kv := range kvs.([]any) {
			pair := kv.([]any)
			if pair[0] != nil && !reflect.TypeOf(pair[0]).Comparable() {
				return nil, fmt.Errorf("py: dict key of Go type %T is not comparable", pair[0])
			}
			out[pair[0]] = pair[1]
		}
		return out, nil
	}
	return o.NewRef(), nil
}

// Int64 returns o as an int64; o must be a Python int that fits.
func (o *Object) Int64() (int64, error) {
	o.live()
	if v := o.pre; v != nil {
		switch v.tag {
		case pvInt:
			return int64(v.a), nil
		case pvTrue:
			return 1, nil
		case pvFalse:
			return 0, nil
		}
	}
	var overflow int32
	n := int64(call2(cLongAsLongLongAndOverflow, o.p, uintptr(unsafe.Pointer(&overflow))))
	if overflow != 0 {
		return 0, errors.New("py: int does not fit in int64")
	}
	if n == -1 && call0(cErrOccurred) != 0 {
		return 0, fetchError()
	}
	return n, nil
}

// Float64 returns float(o) for a float or int.
func (o *Object) Float64() (float64, error) {
	o.live()
	if v := o.pre; v != nil {
		switch v.tag {
		case pvFloat:
			return math.Float64frombits(v.a), nil
		case pvInt:
			return float64(int64(v.a)), nil
		case pvTrue:
			return 1, nil
		case pvFalse:
			return 0, nil
		}
	}
	f := floatAsDouble(o.p)
	if f == -1 && call0(cErrOccurred) != 0 {
		return 0, fetchError()
	}
	return f, nil
}

// GoString returns the contents of a Python str.
func (o *Object) GoString() (string, error) {
	o.live()
	if o.pre != nil && o.pre.tag == pvStr {
		return string(o.pre.payload()), nil
	}
	var n int64
	p := call2(cUnicodeAsUTF8AndSize, o.p, uintptr(unsafe.Pointer(&n)))
	if p == 0 {
		return "", fetchError()
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), n)), nil
}

// Bytes returns a copy of the contents of a bytes or bytearray object.
func (o *Object) Bytes() ([]byte, error) {
	o.live()
	if o.pre != nil && o.pre.tag == pvBytes {
		return append([]byte(nil), o.pre.payload()...), nil
	}
	if int32(call1(cPyKind, o.p)) == kindByteArray {
		n := int(int64(call1(cByteArraySize, o.p)))
		p := call1(cByteArrayAsString, o.p)
		return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(p)), n)...), nil
	}
	var buf uintptr
	var n int64
	if int32(call3(cBytesAsStringAndSize, o.p, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))) < 0 {
		return nil, fetchError()
	}
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(buf)), n)...), nil
}

// UnsafeBytes returns the contents of a bytes object without copying. The slice aliases
// the object's storage: it is valid only while a reference to o is held, and must not be
// modified (bytes are immutable). Unlike the C API, the slice may be read without a
// thread state, e.g. by a goroutine writing it to a socket. o must be a bytes object
// (not a bytearray, whose storage moves when it is resized).
func (o *Object) UnsafeBytes() ([]byte, error) {
	o.live()
	if o.pre != nil && o.pre.tag == pvBytes {
		return o.pre.payload(), nil
	}
	if int32(call1(cPyKind, o.p)) != kindBytes {
		return nil, errors.New("py: UnsafeBytes needs a bytes object")
	}
	var buf uintptr
	var n int64
	if int32(call3(cBytesAsStringAndSize, o.p, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))) < 0 {
		return nil, fetchError()
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(buf)), n), nil
}
