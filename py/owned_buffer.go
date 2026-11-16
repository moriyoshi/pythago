package py

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

// OwnedBuffer is an owned lease over contiguous binary memory. Passing an
// *OwnedBuffer through an aio channel transfers ownership: the sender must stop
// using it after sending. A Go receiver must Close it when finished. A Python
// receiver gets a memoryview and releases it with memoryview.release() or
// ordinary collection.
//
// A lease taken from a Python exporter borrows its memory without copying and
// prevents resizing while the lease is held. Synchronize access to shared memory,
// especially on free-threaded builds. Close Python-backed leases before Finalize.
// An OwnedBuffer must not be copied; slices from Bytes are valid only until Close
// or send.
type OwnedBuffer struct {
	mu       sync.Mutex
	data     []byte
	readOnly bool
	closed   bool
	export   *Buffer // a Python exporter's: its Py_buffer holds the exporter's reference
	// For TakeObject of Go memory: the GoBuffer's pin (held, with the lease, by goBufs
	// until Python frees the object) and its shape, which the glue copies.
	pin   runtime.Pinner
	shape [1]int64
}

// leaseExports are closed Buffers for OwnedBuffer's Python exports, reused: a Buffer's
// Py_buffer and its GC cleanup (which releases an export a lost lease left open) cost
// more than the rest of a lease.
var leaseExports = sync.Pool{New: func() any { return new(Buffer) }}

// formatB is the GoBuffer format of TakeObject's byte views, as a C string.
var formatB = [2]byte{'B', 0}

// NewOwnedBuffer adopts data without copying. The caller must stop using data
// while the lease or a resulting Python memoryview owns it. readOnly controls
// whether Python can write through its memoryview.
func NewOwnedBuffer(data []byte, readOnly bool) *OwnedBuffer {
	return &OwnedBuffer{data: data, readOnly: readOnly}
}

// OwnedBuffer exports o's buffer into a new lease, which keeps o alive and its
// storage pinned until Close. The export must be contiguous; it is writable when
// the exporter allows it (see ReadOnly). Call it with a thread state.
func (o *Object) OwnedBuffer() (*OwnedBuffer, error) {
	o.live()
	export := leaseExports.Get().(*Buffer)
	if err := o.BufferInto(export, false); err != nil {
		leaseExports.Put(export)
		return nil, err
	}
	if _, err := export.Bytes(); err != nil {
		export.Close()
		leaseExports.Put(export)
		return nil, err
	}
	// The export's Py_buffer holds a reference to o (its obj) until it is released.
	return &OwnedBuffer{export: export}, nil
}

// closeExport releases a lease's Python export and recycles its Buffer. Attached.
func closeExport(export *Buffer) {
	export.Close()
	leaseExports.Put(export)
}

// Bytes borrows the memory until Close or ownership transfer. Do not modify the
// slice if ReadOnly is true, or race use of the slice with Close/send. Keep the
// OwnedBuffer reachable while using the slice (for example, with defer b.Close()).
func (b *OwnedBuffer) Bytes() ([]byte, error) {
	if b == nil {
		return nil, errors.New("py: nil buffer")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("py: buffer is closed or transferred")
	}
	if b.export != nil {
		return b.export.Bytes()
	}
	return b.data, nil
}

// Len reads the active export's native byte length, or the owned Go slice length.
func (b *OwnedBuffer) Len() (int, error) {
	if b == nil {
		return 0, errors.New("py: nil buffer")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errors.New("py: buffer is closed or transferred")
	}
	if b.export != nil {
		return b.export.Len(), nil
	}
	return len(b.data), nil
}

// ReadOnly reports whether writing to Bytes is forbidden by the exporter.
func (b *OwnedBuffer) ReadOnly() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.export != nil {
		return b.export.ReadOnly()
	}
	return b.readOnly
}

// Close releases the lease. It is idempotent and may be called from any goroutine.
// Python-backed leases briefly enter Python to release the buffer export.
func (b *OwnedBuffer) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	export := b.export
	b.export, b.data = nil, nil
	b.mu.Unlock()
	if export == nil {
		return nil
	}
	return Do(func() error { closeExport(export); return nil })
}

// TakeObject consumes the lease and returns a flat byte memoryview over its
// memory, which the caller must Close. The memoryview obtains its own export or
// pin before the lease releases its original owner. A nil OwnedBuffer gives None.
// Call it with a thread state.
func (b *OwnedBuffer) TakeObject() (*Object, error) {
	if b == nil {
		return None(), nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, errors.New("py: buffer is closed or transferred")
	}
	b.closed = true
	data, export, readOnly := b.data, b.export, b.readOnly
	b.data, b.export = nil, nil
	b.mu.Unlock()
	var r uintptr
	if export != nil {
		// memoryview(exporter), whose own export keeps it alive once this one is released.
		defer closeExport(export)
		r = call1(cMemoryViewFromObject, export.native().obj)
	} else {
		// A GoBuffer over data, pinned by this lease (which the GoBuffer's token keeps
		// alive until Python frees it), and memoryview(it): the view's export keeps the
		// GoBuffer, which no one else refers to, alive.
		b.shape[0] = int64(len(data))
		gb, err := newGoBuffer(&b.pin, unsafe.Pointer(unsafe.SliceData(data)), uintptr(len(data)), 1, readOnly,
			&formatB[0], b.shape[:])
		if err != nil {
			return nil, err
		}
		r = call1(cMemoryViewFromObject, gb)
		decRef(gb)
	}
	view, err := result(r)
	if err != nil {
		return nil, err
	}
	// Python exporters may be typed or multidimensional; expose a flat byte view.
	if export != nil && (export.ItemSize() != 1 || export.Format() != "B" || len(export.Shape()) != 1) {
		defer view.Close()
		return view.CallMethod("cast", "B")
	}
	return view, nil
}
