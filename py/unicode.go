package py

import (
	"fmt"
	"runtime"
	"unsafe"
)

// Results of nativeStr, cached in Object.str: whether an object is a str cannot
// change (str has no mutable layout, and __class__ assignment keeps it a str).
const (
	strUnknown = iota
	strNative
	strOther
)

// nativeStr reports whether o's Unicode fields can be read natively, the same
// reads String uses: the build's layout was verified at Init and o is a str. The
// type check enters C once per Object; argument values decoded by C need none.
func (o *Object) nativeStr() bool {
	o.live()
	if nativeUnicodeDefinition == nil || o == nil || o.p == 0 {
		return false
	}
	switch o.str.Load() {
	case strNative:
		return true
	case strOther:
		return false
	}
	is := o.IsString()
	if is {
		o.str.Store(strNative)
	} else {
		o.str.Store(strOther)
	}
	return is
}

// UnicodeLen returns the stored Unicode code-point count, bypassing any str
// subclass __len__. It does not request a UTF-8 representation. For a str it
// reads the length natively, without calling into Python.
func (o *Object) UnicodeLen() (int, error) {
	if o.nativeStr() {
		return unicodeLength(o), nil
	}
	return unicodeLenAPI(o)
}

// UnicodeRune returns the stored code point at index, including surrogates.
// Negative and out-of-range indexes raise Python's IndexError. The object must
// be a str; subclass __getitem__ overrides are bypassed. In-range reads of a
// str are native, without calling into Python.
func (o *Object) UnicodeRune(index int) (rune, error) {
	if o.nativeStr() && index >= 0 && index < unicodeLength(o) {
		return unicodeRuneAt(o, index)
	}
	return unicodeRuneAPI(o, index)
}

// UnicodeRunes copies code points directly into a Go UCS-4/rune slice without an
// intermediate UTF-8 encoding. Python surrogate code points are preserved. For a
// str the characters are widened natively, without calling into Python.
func (o *Object) UnicodeRunes() ([]rune, error) {
	if o.nativeStr() {
		return unicodeRunesNative(o)
	}
	return unicodeRunesAPI(o)
}

// UnicodeSlice returns a new reference to the code-point range [start:end].
// It requires Go-style bounds, 0 <= start <= end <= UnicodeLen(), and bypasses
// str subclass overrides. No UTF-8 conversion is performed.
func (o *Object) UnicodeSlice(start, end int) (*Object, error) {
	n, err := o.UnicodeLen()
	if err != nil {
		return nil, err
	}
	if start < 0 || end < start || end > n {
		return nil, fmt.Errorf("py: Unicode slice [%d:%d] out of range [0:%d]", start, end, n)
	}
	p := call3(cUnicodeSubstring, o.p, uintptr(start), uintptr(end))
	runtime.KeepAlive(o)
	return result(p)
}

// The C-API paths: non-str objects (which raise TypeError), out-of-range indexes,
// builds without a verified layout, and the layout check at Init. Tests replace
// them to observe that native reads never reach C.
var (
	unicodeLenAPI   = unicodeLenC
	unicodeRuneAPI  = unicodeRuneC
	unicodeRunesAPI = unicodeRunesC
)

func unicodeLenC(o *Object) (int, error) {
	n := int(call1(cUnicodeGetLength, o.p))
	runtime.KeepAlive(o)
	if n < 0 {
		return 0, fetchError()
	}
	return n, nil
}

func unicodeRuneC(o *Object, index int) (rune, error) {
	value := uint32(call2(cUnicodeReadChar, o.p, uintptr(index)))
	runtime.KeepAlive(o)
	if value == ^uint32(0) {
		return 0, fetchError()
	}
	return rune(value), nil
}

func unicodeRunesC(o *Object) ([]rune, error) {
	n, err := unicodeLenC(o)
	if err != nil {
		return nil, err
	}
	values := make([]rune, n)
	if n == 0 {
		return values, nil
	}
	p := call4(cUnicodeAsUCS4, o.p, uintptr(unsafe.Pointer(unsafe.SliceData(values))), uintptr(n), 0)
	runtime.KeepAlive(values)
	runtime.KeepAlive(o)
	if p == 0 {
		return nil, fetchError()
	}
	return values, nil
}
