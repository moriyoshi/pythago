package py

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/moriyoshi/pythago/internal/layout"
)

// nativeUnicode owns one Python Unicode reference for String. Its only
// per-object state is that reference; length, flags and data pointers are read
// from CPython on every access. It must not be copied. Coordinate reads with Close/TakeObject yourself.
// Len, Rune and Runes need no Python thread state. Other operations do.
type nativeUnicode struct{ owner *Object }

// The selected definition is immutable ABI configuration, not object metadata.
var nativeUnicodeDefinition *layout.Unicode
var nativeUnicodeUnavailable error

// unicodeInto retains o in a closed nativeUnicode. Call with a thread state.
func (o *Object) unicodeInto(view *nativeUnicode) error {
	if view.owner != nil {
		return errors.New("py: unicodeInto needs a closed view")
	}
	if !o.IsString() {
		return errors.New("py: native Unicode view requires str")
	}
	if nativeUnicodeDefinition == nil {
		return nativeUnicodeUnavailable
	}
	view.owner = o.NewRef()
	return nil
}

func (u *nativeUnicode) IsOpen() bool { return u != nil && u.owner != nil }

func (u *nativeUnicode) Len() (int, error) {
	if !u.IsOpen() {
		return 0, errors.New("py: Unicode view is closed")
	}
	return unicodeLength(u.owner), nil
}

func (u *nativeUnicode) Rune(index int) (rune, error) {
	n, err := u.Len()
	if err != nil {
		return 0, err
	}
	if index < 0 || index >= n {
		return 0, fmt.Errorf("py: rune index %d out of range [0:%d]", index, n)
	}
	return unicodeRuneAt(u.owner, index)
}

func (u *nativeUnicode) Runes() ([]rune, error) {
	if !u.IsOpen() {
		return nil, errors.New("py: Unicode view is closed")
	}
	return unicodeRunesNative(u.owner)
}

// The readers below take an open str object and need nativeUnicodeDefinition.
// They need no thread state; o is kept alive across each read.

func unicodeLength(o *Object) int {
	n := *(*int)(unsafe.Add(unsafe.Pointer(o.p), nativeUnicodeDefinition.Length))
	runtime.KeepAlive(o)
	return n
}

// unicodeData reads each representation flag from the native header on every call.
func unicodeData(o *Object) (unsafe.Pointer, uintptr) {
	d := nativeUnicodeDefinition
	p := unsafe.Pointer(o.p)
	kind := uintptr(*(*byte)(unsafe.Add(p, d.KindByte))) & d.KindMask
	kind >>= d.KindShift
	if uintptr(*(*byte)(unsafe.Add(p, d.CompactByte)))&d.CompactMask != 0 {
		if uintptr(*(*byte)(unsafe.Add(p, d.ASCIIByte)))&d.ASCIIMask != 0 {
			return unsafe.Add(p, d.ASCIIData), kind
		}
		return unsafe.Add(p, d.CompactData), kind
	}
	return *(*unsafe.Pointer)(unsafe.Add(p, d.DataPointer)), kind
}

// unicodeRuneAt reads the code point at index, which the caller bounds-checked.
func unicodeRuneAt(o *Object, index int) (rune, error) {
	data, kind := unicodeData(o)
	var r rune
	switch kind {
	case 1:
		r = rune(*(*uint8)(unsafe.Add(data, index)))
	case 2:
		r = rune(*(*uint16)(unsafe.Add(data, uintptr(index)*2)))
	case 4:
		r = rune(*(*uint32)(unsafe.Add(data, uintptr(index)*4)))
	default:
		return 0, errors.New("py: invalid native Unicode kind")
	}
	runtime.KeepAlive(o)
	return r, nil
}

func unicodeRunesNative(o *Object) ([]rune, error) {
	n := unicodeLength(o)
	data, kind := unicodeData(o)
	values := make([]rune, n)
	switch kind {
	case 1:
		for i, v := range unsafe.Slice((*uint8)(data), n) {
			values[i] = rune(v)
		}
	case 2:
		for i, v := range unsafe.Slice((*uint16)(data), n) {
			values[i] = rune(v)
		}
	case 4:
		copy(values, unsafe.Slice((*rune)(data), n))
	default:
		return nil, errors.New("py: invalid native Unicode kind")
	}
	runtime.KeepAlive(o)
	return values, nil
}

func (u *nativeUnicode) GoString() (string, error) {
	if !u.IsOpen() {
		return "", errors.New("py: Unicode view is closed")
	}
	return u.owner.GoString()
}

func (u *nativeUnicode) SliceInto(out *nativeUnicode, start, end int) error {
	if out.owner != nil {
		return errors.New("py: substring destination is open")
	}
	n, err := u.Len()
	if err != nil {
		return err
	}
	if start < 0 || end < start || end > n {
		return fmt.Errorf("py: Unicode slice [%d:%d] out of range [0:%d]", start, end, n)
	}
	o, err := result(call3(cUnicodeSubstring, u.owner.p, uintptr(start), uintptr(end)))
	runtime.KeepAlive(u)
	if err != nil {
		return err
	}
	out.owner = o
	return nil
}

func (u *nativeUnicode) CloneInto(out *nativeUnicode) error {
	if !u.IsOpen() {
		return errors.New("py: Unicode view is closed")
	}
	if out.owner != nil {
		return errors.New("py: clone destination is open")
	}
	out.owner = u.owner.NewRef()
	return nil
}

// TakeObject transfers the owned reference and closes the view without decref.
func (u *nativeUnicode) TakeObject() (*Object, error) {
	if !u.IsOpen() {
		return nil, errors.New("py: Unicode view is closed")
	}
	o := u.owner
	u.owner = nil
	return o, nil
}

func (u *nativeUnicode) Close() {
	if u == nil {
		return
	}
	if o := u.owner; o != nil {
		u.owner = nil
		o.Close()
	}
}

// initializeNativeUnicode selects a pre-derived exact-build definition and checks
// it against canonical/subclass objects before exposing any native views. Unknown
// or mismatching builds can still use the regular Python API; native views fail.
func initializeNativeUnicode() {
	nativeUnicodeDefinition = nil
	nativeUnicodeUnavailable = errors.New("py: native Unicode layout unavailable; regenerate definitions for this Python build")
	addr := symbol("Py_Version")
	if addr == 0 {
		return
	}
	version := *(*uint32)(unsafe.Pointer(addr))
	d, ok := layout.Lookup(version, freeThread)
	if !ok {
		nativeUnicodeUnavailable = fmt.Errorf("py: no native Unicode layout for Python %#x, free-threaded=%t on %s", version, freeThread, runtime.GOARCH)
		return
	}
	if !bufferLayoutMatches(d) {
		nativeUnicodeUnavailable = errors.New("py: native buffer layout mismatch")
		return
	}
	nativeUnicodeDefinition = &d
	for _, text := range []string{"pythago-layout-probe", "café", "日本語", "A🤖𐍈"} {
		o, err := From(text)
		if err == nil {
			err = checkNativeUnicode(o, []rune(text))
		}
		o.Close()
		if err != nil {
			nativeUnicodeDefinition = nil
			nativeUnicodeUnavailable = err
			return
		}
	}
	// Noncompact Unicode uses a separate data pointer, including ASCII subclasses.
	for _, literal := range []string{"'subclass-probe'", "'A🤖𐍈'"} {
		o, err := Eval("type('_PythagoUnicodeProbe',(str,),{})(" + literal + ")")
		if err == nil {
			// The expected values come from the C API, never from the native reads under test.
			values, e := unicodeRunesC(o)
			err = e
			if err == nil {
				err = checkNativeUnicode(o, values)
			}
		}
		o.Close()
		if err != nil {
			nativeUnicodeDefinition = nil
			nativeUnicodeUnavailable = err
			return
		}
	}
}

func checkNativeUnicode(o *Object, want []rune) error {
	view := nativeUnicode{owner: o} // borrowed only for this initialization check
	n, err := view.Len()
	if err != nil || n != len(want) {
		return errors.New("py: native Unicode length layout mismatch")
	}
	data, kind := unicodeData(o)
	if data == nil || (kind != 1 && kind != 2 && kind != 4) {
		return errors.New("py: native Unicode data layout mismatch")
	}
	values, err := view.Runes()
	if err != nil {
		return err
	}
	for i, v := range values {
		if v != want[i] {
			return errors.New("py: native Unicode character layout mismatch")
		}
	}
	return nil
}

func bufferLayoutMatches(d layout.Unicode) bool {
	offsets := [11]uintptr{unsafe.Offsetof(pyBuffer{}.buf), unsafe.Offsetof(pyBuffer{}.obj), unsafe.Offsetof(pyBuffer{}.len), unsafe.Offsetof(pyBuffer{}.itemsize), unsafe.Offsetof(pyBuffer{}.readonly), unsafe.Offsetof(pyBuffer{}.ndim), unsafe.Offsetof(pyBuffer{}.format), unsafe.Offsetof(pyBuffer{}.shape), unsafe.Offsetof(pyBuffer{}.strides), unsafe.Offsetof(pyBuffer{}.suboffsets), unsafe.Offsetof(pyBuffer{}.internal)}
	return d.BufferSize == unsafe.Sizeof(pyBuffer{}) && d.BufferOffsets == offsets
}
