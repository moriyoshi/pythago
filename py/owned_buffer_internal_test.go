package py

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The Go allocations of an OwnedBuffer's two directions, as measured on 2026-11-20
// (3.14t):
//   - 1 for a lease over a Python exporter (Object.OwnedBuffer, then Close inside
//     Python): the lease. The export's Buffer is reused (leaseExports), and the lease
//     holds no Object of its own: the export's Py_buffer refers to the exporter.
//   - 5 for a lease over Go memory turned into a memoryview (NewOwnedBuffer, TakeObject,
//     and the view's Close): the lease, the pin's first Pin, and the view's Object. The
//     memoryview is made with the C API from a GoBuffer that gets no Object.
//
// Before, they were 9 and 20: a new Buffer with its Py_buffer and GC cleanup, and a
// NewRef of the exporter; builtins and memoryview looked up and called through Objects,
// with NewBuffer's Pinner, shape slice, format C string and format maps.
const (
	maxPythonLeaseAllocs = 1
	maxGoLeaseAllocs     = 5
)

func TestOwnedBufferAllocs(t *testing.T) {
	if err := Do(func() error {
		exporter, err := Eval(`bytearray(b"lease")`)
		if err != nil {
			return err
		}
		defer exporter.Close()
		python := func() {
			lease, err := exporter.OwnedBuffer()
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
		}
		data := []byte("go memory")
		goLease := func() {
			view, err := NewOwnedBuffer(data, true).TakeObject()
			if err != nil {
				t.Fatal(err)
			}
			view.Close()
		}
		for range 50 { // warm: the pools, goBufs
			python()
			goLease()
		}
		if n := testing.AllocsPerRun(1000, python); n > maxPythonLeaseAllocs {
			t.Errorf("a lease over a Python exporter allocates %v times, want at most %d", n, maxPythonLeaseAllocs)
		}
		if n := testing.AllocsPerRun(1000, goLease); n > maxGoLeaseAllocs {
			t.Errorf("a Go lease taken as a memoryview allocates %v times, want at most %d", n, maxGoLeaseAllocs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A closed lease's Buffer serves the next lease: the closed one must see nothing of it
// (closed), and the new one its own exporter's memory, also when the first lease was
// turned into a memoryview, which keeps working after its lease's Buffer is reused: it
// holds an export of its own (its exporter cannot be resized meanwhile), not a copy of
// the lease's.
func TestOwnedBufferExportReuse(t *testing.T) {
	if err := Do(func() error {
		a, err := Eval(`bytearray(b"first")`)
		if err != nil {
			return err
		}
		defer a.Close()
		b, err := Eval(`bytearray(b"second")`)
		if err != nil {
			return err
		}
		defer b.Close()
		for round := range 20 {
			la, err := a.OwnedBuffer()
			if err != nil {
				return err
			}
			var view *Object
			if round%2 == 0 {
				la.Close()
			} else if view, err = la.TakeObject(); err != nil {
				return err
			}
			lb, err := b.OwnedBuffer()
			if err != nil {
				return err
			}
			if data, err := la.Bytes(); err == nil {
				t.Fatalf("round %d: the closed lease still reads %q", round, data)
			}
			if data, err := lb.Bytes(); err != nil || string(data) != "second" {
				t.Fatalf("round %d: the new lease reads %q, %v; want its own exporter's bytes", round, data, err)
			}
			if view != nil {
				// The view holds an export of its own: its exporter cannot be resized.
				if r, err := a.CallMethod("append", 0); err == nil {
					r.Close()
					t.Fatalf("round %d: the first lease's memoryview holds no export of its exporter (it could be resized)", round)
				} else if !strings.Contains(err.Error(), "BufferError") {
					return err
				}
				got, err := view.CallMethod("tobytes")
				if err != nil {
					return err
				}
				s, _ := got.Bytes()
				got.Close()
				view.Close()
				if string(s) != "first" {
					t.Fatalf("round %d: the first lease's memoryview reads %q", round, s)
				}
			}
			lb.Close()
		}
		// Both exporters are free again: a resize needs no export open.
		_, err = a.CallMethod("append", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// NewBuffer's format for every element type matches Python's struct module (the same
// item size as Go's), and PEP 3118's Zf and Zd for complex numbers, which struct lacks.
func TestFormatOfEveryElem(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[int8](), reflect.TypeFor[int16](), reflect.TypeFor[int32](), reflect.TypeFor[int64](),
		reflect.TypeFor[int](), reflect.TypeFor[uint8](), reflect.TypeFor[uint16](), reflect.TypeFor[uint32](),
		reflect.TypeFor[uint64](), reflect.TypeFor[uint](), reflect.TypeFor[uintptr](), reflect.TypeFor[float32](),
		reflect.TypeFor[float64](), reflect.TypeFor[complex64](), reflect.TypeFor[complex128](), reflect.TypeFor[bool](),
	}
	if err := Do(func() error {
		for _, ty := range types {
			f, err := formatOf(ty)
			if err != nil {
				return err
			}
			if ty.Kind() == reflect.Complex64 || ty.Kind() == reflect.Complex128 {
				// struct has no complex formats: PEP 3118's Zf and Zd, by size.
				if want := map[uintptr]string{8: "Zf", 16: "Zd"}[ty.Size()]; f != want {
					t.Errorf("%s: format %q, want %q", ty, f, want)
				}
				continue
			}
			size, err := Eval(fmt.Sprintf("__import__('struct').calcsize(%q)", f))
			if err != nil {
				return fmt.Errorf("%s: format %q: %w", ty, f, err)
			}
			n, _ := size.Int64()
			size.Close()
			if uintptr(n) != ty.Size() {
				t.Errorf("%s: format %q has item size %d, Go's is %d", ty, f, n, ty.Size())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
