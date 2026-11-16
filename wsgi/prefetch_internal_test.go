package wsgi

import (
	"bytes"
	"io"
	"testing"
)

// firstRead records the size of the first buffer it is asked to fill.
type firstRead struct {
	r     io.Reader
	first int
}

func (f *firstRead) Read(p []byte) (int, error) {
	if f.first == 0 {
		f.first = len(p)
	}
	return f.r.Read(p)
}

// TestPrefetchGrows: a declared 64 KiB body is not given 64 KiB before any of it arrives.
func TestPrefetchGrows(t *testing.T) {
	r := &firstRead{r: bytes.NewReader([]byte("x"))}
	if _, err := prefetch(r, 64<<10); err == nil {
		t.Error("a body shorter than declared was accepted")
	}
	if r.first > 4096 {
		t.Errorf("first read into %d bytes for a declared 64 KiB body", r.first)
	}
	b, err := prefetch(bytes.NewReader(bytes.Repeat([]byte("y"), 1000)), 1000)
	if err != nil || len(b) != 1000 {
		t.Errorf("exact body: %d %v", len(b), err)
	}
}
