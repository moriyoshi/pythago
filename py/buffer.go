package py

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Buffer is a view of a Python object's memory through the buffer protocol: bytes,
// bytearray, memoryview, array.array, numpy arrays and anything else that exports one. The
// memory is shared, not copied: [Buffer.Bytes] and [View] return Go slices over it, and
// writes through them (on a writable buffer) are visible to Python, and the reverse.
//
// The exporter keeps the memory in place until Close: a bytearray or numpy array cannot be
// resized meanwhile. Slices taken from a Buffer are valid only while it is open, so keep
// the Buffer reachable while using them (runtime.KeepAlive) and Close it afterwards, with
// a thread state, as for [Object.Close]. A Buffer that becomes unreachable unclosed is
// released by the next goroutine that enters Python.
//
// Neither side locks the memory: synchronize Go and Python access yourself, notably on
// free-threaded builds.
type Buffer struct {
	anchor *bufAnchor // its GC cleanup releases the export or frees the memory
}

// bufState is a Buffer's Py_buffer, in C memory so that it never moves. It is allocated
// once per Buffer and reused by every export into it ([Object.BufferInto]).
type bufState struct {
	view     uintptr // Py_buffer*
	exported atomic.Bool
}

// bufAnchor carries the GC cleanup, so that a Buffer can also live inside another struct
// (zero value, then BufferInto).
type bufAnchor struct{ st *bufState }

// bufferCleanup runs when a Buffer becomes unreachable: an open export is released by the
// next goroutine that enters Python (that needs a thread state); otherwise only the memory
// is freed, which the raw allocator allows without one.
func bufferCleanup(st *bufState) {
	if st.exported.Load() {
		deferRelease(st.view)
	} else {
		call1(cMemRawFree, st.view)
	}
}

func (b *Buffer) state() (*bufState, error) {
	if b.anchor == nil {
		view := call2(cMemRawCalloc, 1, unsafe.Sizeof(pyBuffer{}))
		if view == 0 {
			return nil, errors.New("py: out of memory")
		}
		b.anchor = &bufAnchor{st: &bufState{view: view}}
		runtime.AddCleanup(b.anchor, bufferCleanup, b.anchor.st)
	}
	return b.anchor.st, nil
}

func (b *Buffer) open() bool { return b.anchor != nil && b.anchor.st.exported.Load() }

// Py_buffer is in the stable ABI (3.11+), so its layout is fixed.
type pyBuffer struct {
	buf        unsafe.Pointer
	obj        uintptr
	len        int64
	itemsize   int64
	readonly   int32
	ndim       int32
	format     *byte
	shape      *int64
	strides    *int64
	suboffsets *int64
	internal   uintptr
}

const (
	bufWritable = 0x0001
	bufFormat   = 0x0004
	bufND       = 0x0008
	bufStrides  = 0x0010 | bufND
)

// Buffer exports o's memory, writable or read-only. It fails if o does not support the
// buffer protocol, if writable is requested of a read-only buffer (bytes), or if the buffer
// needs suboffsets (PIL-style indirect arrays).
func (o *Object) Buffer(writable bool) (*Buffer, error) {
	b := new(Buffer)
	if err := o.BufferInto(b, writable); err != nil {
		return nil, err
	}
	return b, nil
}

// BufferInto exports o's memory into b, as [Object.Buffer] does, reusing b's allocations:
// code that borrows buffers repeatedly (a read loop) keeps one Buffer and closes it after
// each use. b must be closed; a zero Buffer is fine.
func (o *Object) BufferInto(b *Buffer, writable bool) error {
	o.live()
	st, err := b.state()
	if err != nil {
		return err
	}
	if st.exported.Load() {
		return errors.New("py: BufferInto needs a closed Buffer")
	}
	flags := uintptr(bufStrides | bufFormat)
	if writable {
		flags |= bufWritable
	}
	if int32(call3(cObjectGetBuffer, o.p, st.view, flags)) != 0 {
		return fetchError()
	}
	st.exported.Store(true)
	v := (*pyBuffer)(unsafe.Pointer(st.view))
	if v.suboffsets != nil {
		b.Close()
		return errors.New("py: buffers with suboffsets are not supported")
	}
	if v.len < 0 || v.itemsize <= 0 || v.ndim < 0 || (v.ndim > 0 && v.shape == nil) || (v.len > 0 && v.buf == nil) {
		b.Close()
		return errors.New("py: invalid buffer metadata")
	}
	return nil
}

// bufferFormat reads the native format, using constants for one-character formats.
func bufferFormat(p *byte) string {
	if p == nil {
		return "B"
	}
	if *p != 0 && *(*byte)(unsafe.Add(unsafe.Pointer(p), 1)) == 0 {
		if s, ok := oneCharFormats[*p]; ok {
			return s
		}
	}
	return goString(p)
}

var oneCharFormats = func() map[byte]string {
	m := map[byte]string{}
	for _, c := range "bBhHiIlLqQnNfdc?e" {
		m[byte(c)] = string(c)
	}
	return m
}()

// releaseBuffer releases a Py_buffer (with a thread state) and frees it.
func releaseBuffer(view uintptr) {
	call1(cBufferRelease, view)
	call1(cMemRawFree, view)
}

// Close releases the export; slices taken from the buffer must no longer be used. The
// Buffer can be exported into again with [Object.BufferInto]. Closing twice does nothing.
func (b *Buffer) Close() {
	if b == nil || b.anchor == nil {
		return
	}
	if st := b.anchor.st; st.exported.CompareAndSwap(true, false) {
		call1(cBufferRelease, st.view)
	}
}

// native returns the retained Py_buffer. Callers must keep b alive while reading
// it and must not race any access with Close or a new BufferInto export.
func (b *Buffer) native() *pyBuffer {
	if b == nil || !b.open() {
		return nil
	}
	return (*pyBuffer)(unsafe.Pointer(b.anchor.st.view))
}

// Len is the native export's byte length, or zero for a closed Buffer.
func (b *Buffer) Len() int {
	v := b.native()
	if v == nil {
		return 0
	}
	n := int(v.len)
	runtime.KeepAlive(b)
	return n
}

// ItemSize is the native element size, or zero for a closed Buffer.
func (b *Buffer) ItemSize() int {
	v := b.native()
	if v == nil {
		return 0
	}
	n := int(v.itemsize)
	runtime.KeepAlive(b)
	return n
}

// Format reads the native struct-format descriptor; a closed Buffer returns "".
func (b *Buffer) Format() string {
	v := b.native()
	if v == nil {
		return ""
	}
	format := bufferFormat(v.format)
	runtime.KeepAlive(b)
	return format
}

func (b *Buffer) ReadOnly() bool {
	v := b.native()
	if v == nil {
		return false
	}
	ro := v.readonly != 0
	runtime.KeepAlive(b)
	return ro
}

// Shape returns a read-only native metadata view, valid only while the export is
// open. It must not be modified. Nil represents a scalar or a closed Buffer.
func (b *Buffer) Shape() []int {
	v := b.native()
	if v == nil || v.ndim == 0 {
		return nil
	}
	shape := unsafe.Slice((*int)(unsafe.Pointer(v.shape)), int(v.ndim))
	runtime.KeepAlive(b)
	return shape
}

// Strides returns a read-only native metadata view, valid only while open.
// It must not be modified. Nil means C-contiguous, scalar, or closed.
func (b *Buffer) Strides() []int {
	v := b.native()
	if v == nil || v.strides == nil || v.ndim == 0 {
		return nil
	}
	strides := unsafe.Slice((*int)(unsafe.Pointer(v.strides)), int(v.ndim))
	runtime.KeepAlive(b)
	return strides
}

func contiguous(v *pyBuffer) bool {
	if v.strides == nil {
		return true
	}
	shape := unsafe.Slice(v.shape, int(v.ndim))
	strides := unsafe.Slice(v.strides, int(v.ndim))
	want := v.itemsize
	for i := len(shape) - 1; i >= 0; i-- {
		if shape[i] == 0 {
			return true
		}
		if shape[i] > 1 && strides[i] != want {
			return false
		}
		want *= shape[i]
	}
	return true
}

func (b *Buffer) Contiguous() bool {
	v := b.native()
	if v == nil {
		return false
	}
	ok := contiguous(v)
	runtime.KeepAlive(b)
	return ok
}

// Bytes reads pointer/length/strides directly from the retained native export.
// The borrowed slice remains valid only while b is reachable and open.
func (b *Buffer) Bytes() ([]byte, error) {
	v := b.native()
	if v == nil {
		return nil, errors.New("py: buffer is closed")
	}
	if !contiguous(v) {
		return nil, errors.New("py: buffer is not C-contiguous")
	}
	data := unsafe.Slice((*byte)(v.buf), int(v.len))
	runtime.KeepAlive(b)
	return data, nil
}

// Elem is a Go type that [View] can map a buffer's elements to.
type Elem interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~int |
		~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uint | ~uintptr |
		~float32 | ~float64 | ~complex64 | ~complex128 | ~bool
}

// View returns the elements of a contiguous buffer as a []T, without copying. T must match
// the buffer's format in kind (signed, unsigned, floating-point, complex, bool) and size:
// "d" is float64, "i" int32, "q" or "l" int64 (as the buffer's item size says), "B" uint8.
// Little-endian ("<"), native ("@") and standard ("=") formats are accepted on these
// little-endian targets; big-endian ones only for 1-byte elements.
func View[T Elem](b *Buffer) ([]T, error) {
	v := b.native()
	if v == nil {
		return nil, errors.New("py: buffer is closed")
	}
	if !contiguous(v) {
		return nil, errors.New("py: buffer is not C-contiguous")
	}
	t := reflect.TypeFor[T]()
	format, itemSize, size := bufferFormat(v.format), int(v.itemsize), int(v.len)
	kind, err := formatKind(format, itemSize)
	if err != nil {
		return nil, err
	}
	if kind != elemKind(t.Kind()) || int(t.Size()) != itemSize {
		return nil, fmt.Errorf("py: buffer format %q (%d-byte items) does not match %s", format, itemSize, t)
	}
	if size%itemSize != 0 {
		return nil, fmt.Errorf("py: buffer size %d is not a multiple of its item size %d", size, itemSize)
	}
	if uintptr(v.buf)%uintptr(t.Align()) != 0 {
		return nil, fmt.Errorf("py: buffer memory is not aligned for %s", t)
	}
	data := unsafe.Slice((*T)(v.buf), size/itemSize)
	runtime.KeepAlive(b)
	return data, nil
}

// WithView calls fn with o's buffer as a []T (see [View]) and releases the buffer when fn
// returns, on error or panic too. It is the safe way to borrow Python memory: the slice
// must not be kept past fn, and while fn runs the exporter keeps the memory in place (a
// bytearray or numpy array cannot be resized). Call it with a thread state, inside [Do] or
// a Go function called from Python. fn runs attached; it may use [Release] around long
// computations, but Python code can then write the memory concurrently.
func WithView[T Elem](o *Object, writable bool, fn func(v []T) error) error {
	b, err := o.Buffer(writable)
	if err != nil {
		return err
	}
	defer b.Close()
	v, err := View[T](b)
	if err != nil {
		return err
	}
	return fn(v)
}

type elemClass int

const (
	elemSigned elemClass = iota + 1
	elemUnsigned
	elemFloat
	elemComplex
	elemBoolean
)

func elemKind(k reflect.Kind) elemClass {
	switch k {
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int:
		return elemSigned
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint, reflect.Uintptr:
		return elemUnsigned
	case reflect.Float32, reflect.Float64:
		return elemFloat
	case reflect.Complex64, reflect.Complex128:
		return elemComplex
	case reflect.Bool:
		return elemBoolean
	}
	return 0
}

// formatKind classifies a single-element struct format; sizes are checked against the
// buffer's item size, which also settles "l"/"L" (8 bytes native, 4 standard).
func formatKind(format string, itemSize int) (elemClass, error) {
	f := format
	if f != "" {
		switch f[0] {
		case '@', '=', '<':
			f = f[1:]
		case '>', '!':
			if itemSize != 1 {
				return 0, fmt.Errorf("py: big-endian buffer format %q is not supported", format)
			}
			f = f[1:]
		}
	}
	switch f {
	case "b", "h", "i", "l", "q", "n":
		return elemSigned, nil
	case "B", "H", "I", "L", "Q", "N", "c":
		return elemUnsigned, nil
	case "f", "d":
		return elemFloat, nil
	case "Zf", "Zd":
		return elemComplex, nil
	case "?":
		return elemBoolean, nil
	}
	return 0, fmt.Errorf("py: unsupported buffer format %q", format)
}

// Go → Python.

// goBuffers pins the Go memory of live GoBuffer objects, by token.
var (
	goBufMu    sync.Mutex // also makes drainGoBuffers the glue's single consumer
	goBufs     = map[uint64]*runtime.Pinner{}
	goBufToken uint64
	goBufEmpty byte // the address an empty buffer points at
)

// NewBuffer returns a Python object that exports data's memory through the buffer
// protocol, without copying: memoryview(obj), numpy.asarray(obj) and other consumers see
// and (unless readOnly) write the Go slice itself. shape gives the dimensions in C
// (row-major) order; it defaults to one dimension of len(data), and its product must be
// len(data).
//
// The slice's backing array stays pinned while Python holds the object or views of it.
// Go may keep using the slice, but must not grow it past its capacity expecting Python to
// follow (append moves the data). Neither side locks the memory.
func NewBuffer[T Elem](data []T, readOnly bool, shape ...int) (*Object, error) {
	// Long-running Python calls may export many buffers without re-entering Do.
	// Reclaim previously released pins here so streaming exports stay bounded.
	drainGoBuffers()
	t := reflect.TypeFor[T]()
	format, err := formatOf(t)
	if err != nil {
		return nil, err
	}
	if len(shape) == 0 {
		shape = []int{len(data)}
	}
	if len(shape) > 64 {
		return nil, fmt.Errorf("py: %d dimensions is more than the buffer protocol's 64", len(shape))
	}
	n := 1
	cshape := make([]int64, len(shape))
	for i, d := range shape {
		if d < 0 {
			return nil, fmt.Errorf("py: negative dimension %d", d)
		}
		n *= d
		cshape[i] = int64(d)
	}
	if n != len(data) {
		return nil, fmt.Errorf("py: shape %v holds %d elements, not len(data) = %d", shape, n, len(data))
	}
	r, err := newGoBuffer(new(runtime.Pinner), unsafe.Pointer(unsafe.SliceData(data)), uintptr(len(data))*t.Size(),
		t.Size(), readOnly, cString(format), cshape)
	if err != nil {
		return nil, err
	}
	return own(r), nil
}

// newGoBuffer returns a new reference to a GoBuffer over the size bytes at p (nil when
// empty), with format (a C string) and shape (copied by the glue). pin pins p until
// Python frees the object, which drainGoBuffers learns; it is not used for anything
// else meanwhile.
func newGoBuffer(pin *runtime.Pinner, p unsafe.Pointer, size, itemsize uintptr, readOnly bool, format *byte, shape []int64) (uintptr, error) {
	if size == 0 {
		p = unsafe.Pointer(&goBufEmpty)
	} else {
		pin.Pin(p)
	}
	goBufMu.Lock()
	goBufToken++
	token := goBufToken
	goBufs[token] = pin
	goBufMu.Unlock()
	ro := uintptr(0)
	if readOnly {
		ro = 1
	}
	r := call8(cGoBufNew, uintptr(p), size, itemsize, ro,
		uintptr(unsafe.Pointer(format)), uintptr(len(shape)), uintptr(unsafe.Pointer(&shape[0])), uintptr(token))
	runtime.KeepAlive(format)
	if r == 0 {
		goBufMu.Lock()
		delete(goBufs, token)
		goBufMu.Unlock()
		pin.Unpin()
		return 0, fetchError()
	}
	return r, nil
}

// formatOf is the struct format of a Go element type (native byte order).
func formatOf(t reflect.Type) (string, error) {
	size := t.Size()
	switch elemKind(t.Kind()) {
	case elemSigned:
		return formatBySize(size, "b", "h", "i", "q"), nil
	case elemUnsigned:
		return formatBySize(size, "B", "H", "I", "Q"), nil
	case elemFloat:
		return formatBySize(size, "", "", "f", "d"), nil
	case elemComplex:
		return formatBySize(size/2, "", "", "Zf", "Zd"), nil
	case elemBoolean:
		return "?", nil
	}
	return "", fmt.Errorf("py: no buffer format for %s", t)
}

// formatBySize picks the format for an element of size 1, 2, 4 or 8 bytes ("" for any
// other size, as the map lookups this replaces gave).
func formatBySize(size uintptr, f1, f2, f4, f8 string) string {
	switch size {
	case 1:
		return f1
	case 2:
		return f2
	case 4:
		return f4
	case 8:
		return f8
	}
	return ""
}

var goBufReleased *atomic.Int64 // the glue's count of released tokens

var drainTokens [64]uint64 // drainGoBuffers' batch, under goBufMu

func goBuffersReleased() bool {
	if goBufReleased == nil {
		return false
	}
	return goBufReleased.Load() > 0
}

// drainGoBuffers unpins the memory of GoBuffer objects Python has freed.
func drainGoBuffers() {
	if !goBuffersReleased() {
		return
	}
	goBufMu.Lock()
	defer goBufMu.Unlock()
	tokens := &drainTokens // under goBufMu: a local would escape to the heap on every drain
	for {
		n := int(call2(cGoBufDrain, uintptr(unsafe.Pointer(&tokens[0])), uintptr(len(tokens))))
		for _, tk := range tokens[:n] {
			if pin := goBufs[tk]; pin != nil {
				pin.Unpin()
				delete(goBufs, tk)
			}
		}
		if n < len(tokens) {
			return
		}
	}
}

func liveGoBuffers() int64 {
	drainGoBuffers()
	goBufMu.Lock()
	defer goBufMu.Unlock()
	return int64(len(goBufs))
}
