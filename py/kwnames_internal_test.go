package py

import (
	"testing"
	"unsafe"
)

// nameFrame is a call frame whose first keyword name C "decoded" to the bytes b. kwName
// reads such a name from the frame alone, so these tests need no interpreter.
func nameFrame(b []byte) *callFrame {
	v := &pyValue{tag: pvStr, a: uint64(uintptr(unsafe.Pointer(unsafe.SliceData(b)))), b: uint64(len(b))}
	return &callFrame{nkw: 1, names: uintptr(unsafe.Pointer(v))}
}

func kwNameSet(b []byte) uint64 {
	return uint64(uintptr(unsafe.Pointer(unsafe.SliceData(b)))) * 0x9e3779b97f4a7c15 >> 56
}

// A name whose memory now holds other contents (a str that died and a new one in its
// place) is read again, not taken from the cache.
func TestKwNameRereadsChangedContents(t *testing.T) {
	b := []byte("timeout")
	c := nameFrame(b)
	for _, want := range []string{"timeout", "timeouT", "encodin"} {
		copy(b, want)
		for range 2 { // the second call takes the cache's entry, if any
			if got, err := kwName(c, 0, new(int64)); err != nil || got != want {
				t.Fatalf("kwName = %q, %v; the name's memory holds %q", got, err, want)
			}
		}
	}
}

// Two names whose addresses share a cache set are both kept: calls that alternate
// between them copy no names once each has been seen.
func TestKwNameSetHoldsTwo(t *testing.T) {
	var bufs [][]byte
	seen := map[uint64][]byte{}
	var x, y []byte
	for x == nil {
		b := []byte("name00")
		b[4], b[5] = byte('a'+len(bufs)%26), byte('a'+len(bufs)/26%26)
		bufs = append(bufs, b) // keep each alive, so that addresses stay distinct
		if o, ok := seen[kwNameSet(b)]; ok {
			x, y = o, b
		}
		seen[kwNameSet(b)] = b
	}
	cx, cy := nameFrame(x), nameFrame(y)
	n := new(int64)
	call := func() {
		if s, _ := kwName(cx, 0, n); s != string(x) {
			t.Fatalf("kwName = %q, want %q", s, x)
		}
		if s, _ := kwName(cy, 0, n); s != string(y) {
			t.Fatalf("kwName = %q, want %q", s, y)
		}
	}
	call()
	if got := testing.AllocsPerRun(100, call); got != 0 {
		t.Errorf("two names %q and %q in one cache set: %.2f allocations per pair of calls, want 0", x, y, got)
	}
}
