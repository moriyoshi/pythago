package py

import (
	"testing"
	"time"
	"unsafe"
)

func BenchmarkBufferExportNative(b *testing.B) {
	if err := Do(func() error {
		o, err := From([]byte{1, 2, 3})
		if err != nil {
			return err
		}
		defer o.Close()
		var view Buffer
		if err := o.BufferInto(&view, false); err != nil {
			return err
		}
		view.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := o.BufferInto(&view, false); err != nil {
				return err
			}
			view.Close()
		}
		b.StopTimer()
		return nil
	}); err != nil {
		b.Fatal(err)
	}
}

func TestNativeReadsNeedNoPythonAdmission(t *testing.T) {
	var text nativeUnicode
	var buffer *Buffer
	if err := Do(func() error {
		o, err := From("Aé🤖")
		if err != nil {
			return err
		}
		defer o.Close()
		if err := o.unicodeInto(&text); err != nil {
			return err
		}
		o, err = From([]byte{1, 2, 3})
		if err != nil {
			return err
		}
		defer o.Close()
		buffer, err = o.Buffer(false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer Do(func() error { text.Close(); buffer.Close(); return nil })
	count := cap(slots)
	for range count {
		slots <- struct{}{}
	}
	released := false
	release := func() {
		if !released {
			released = true
			for range count {
				<-slots
			}
		}
	}
	defer release()
	done := make(chan bool, 1)
	go func() {
		n, e1 := text.Len()
		r, e2 := text.Rune(2)
		rs, e3 := text.Runes()
		data, e4 := buffer.Bytes()
		done <- e1 == nil && e2 == nil && e3 == nil && e4 == nil && n == 3 && r == '🤖' && len(rs) == 3 && len(data) == 3
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Error("incorrect native data")
		}
	case <-time.After(time.Second):
		release()
		<-done
		t.Error("native access tried to enter Python")
	}
}

func TestBufferMetadataComesFromNativeExport(t *testing.T) {
	if err := Do(func() error {
		o, err := From([]byte{1, 2, 3})
		if err != nil {
			return err
		}
		defer o.Close()
		b, err := o.Buffer(false)
		if err != nil {
			return err
		}
		defer b.Close()
		record := (*pyBuffer)(unsafe.Pointer(b.anchor.st.view))
		length, readonly := record.len, record.readonly
		record.len, record.readonly = 2, 0
		defer func() { record.len, record.readonly = length, readonly }()
		data, err := b.Bytes()
		if err != nil {
			return err
		}
		if b.Len() != 2 || len(data) != 2 || b.ReadOnly() {
			t.Error("buffer metadata was cached")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBufferReuseAddsNoMetadataAllocations(t *testing.T) {
	if err := Do(func() error {
		o, err := From([]byte{1, 2, 3})
		if err != nil {
			return err
		}
		defer o.Close()
		var b Buffer
		if err := o.BufferInto(&b, false); err != nil {
			return err
		}
		b.Close()
		direct := testing.AllocsPerRun(100, func() {
			if int32(call(cObjectGetBuffer, o.p, b.anchor.st.view, uintptr(bufStrides|bufFormat))) != 0 {
				t.Fatal("GetBuffer failed")
			}
			call(cBufferRelease, b.anchor.st.view)
		})
		reused := testing.AllocsPerRun(100, func() {
			if err := o.BufferInto(&b, false); err != nil {
				t.Fatal(err)
			}
			b.Close()
		})
		if reused > direct {
			t.Errorf("buffer reuse allocates %v, direct boundary allocates %v", reused, direct)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
