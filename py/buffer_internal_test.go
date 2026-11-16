package py

import "testing"

func TestFormatKind(t *testing.T) {
	for _, c := range []struct {
		format string
		size   int
		want   elemClass
		ok     bool
	}{
		{"d", 8, elemFloat, true}, {"<d", 8, elemFloat, true}, {"=l", 4, elemSigned, true},
		{"@L", 8, elemUnsigned, true}, {"B", 1, elemUnsigned, true}, {">B", 1, elemUnsigned, true},
		{"?", 1, elemBoolean, true}, {"Zd", 16, elemComplex, true},
		{">d", 8, 0, false}, {"!i", 4, 0, false}, {"e", 2, 0, false}, {"3d", 24, 0, false}, {"T{d}", 8, 0, false},
	} {
		got, err := formatKind(c.format, c.size)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("formatKind(%q, %d) = %v, %v", c.format, c.size, got, err)
		}
	}
}

func TestNewBufferReclaimsPinsWhileAttached(t *testing.T) {
	if err := Do(func() error {
		drainGoBuffers()
		goBufMu.Lock()
		before := len(goBufs)
		goBufMu.Unlock()
		for range 100 {
			o, err := NewBuffer([]byte{1, 2}, true)
			if err != nil {
				return err
			}
			o.Close()
		}
		// Do not call ReadStats: it would itself drain and hide unbounded retention.
		goBufMu.Lock()
		after := len(goBufs)
		goBufMu.Unlock()
		if after > before+1 {
			t.Errorf("released exports retained %d pins", after-before)
		}
		drainGoBuffers()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
