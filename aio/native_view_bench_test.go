package aio

import (
	"sync"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

// The APIAndDo variants add a Python entry (py.Do) to each access, as the access
// path before py.String had. The Object Unicode accessors they call now read a str
// natively too, so they measure the entry, not a C call. Native readers have no
// metadata cache.
func BenchmarkNativeViewAccess(b *testing.B) {
	text := "Aé🤖日本語"
	s, err := py.NewString(text)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	var object *py.Object
	var buffer *py.OwnedBuffer
	if err := py.Do(func() error {
		var err error
		object, err = py.From(text)
		if err != nil {
			return err
		}
		o, err := py.From([]byte{1, 2, 3, 4})
		if err != nil {
			return err
		}
		defer o.Close()
		buffer, err = bufferFromObject(o)
		return err
	}); err != nil {
		b.Fatal(err)
	}
	defer py.Do(func() error { object.Close(); return nil })
	defer buffer.Close()
	var controlMu sync.Mutex
	b.Run("Len/Native", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.Len(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Len/APIAndDo", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if err := py.Do(func() error { controlMu.Lock(); defer controlMu.Unlock(); _, err := object.UnicodeLen(); return err }); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Rune/Native", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.Rune(2); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Rune/APIAndDo", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if err := py.Do(func() error { controlMu.Lock(); defer controlMu.Unlock(); _, err := object.UnicodeRune(2); return err }); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Runes/Native", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.Runes(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Runes/APIAndDo", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if err := py.Do(func() error { controlMu.Lock(); defer controlMu.Unlock(); _, err := object.UnicodeRunes(); return err }); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Buffer/Bytes", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := buffer.Bytes(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Buffer/Len", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := buffer.Len(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Buffer/ReadOnly", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			_ = buffer.ReadOnly()
		}
	})
}
