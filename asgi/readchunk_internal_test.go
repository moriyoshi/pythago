package asgi

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

// TestReadChunkGrows: a request body is not given a whole chunk before any of it arrives,
// and readChunk still returns full chunks and their end.
func TestReadChunkGrows(t *testing.T) {
	r := &firstRead{r: bytes.NewReader([]byte("x"))}
	b, more, err := readChunk(r, 64<<10)
	if err != nil || string(b) != "x" || more {
		t.Errorf("one byte: %q %v %v", b, more, err)
	}
	if r.first > 4096 {
		t.Errorf("first read into %d bytes for a 64 KiB chunk", r.first)
	}
	data := bytes.Repeat([]byte("0123456789"), 20000) // 200,000 bytes: 3 full chunks and a part
	src := io.MultiReader(bytes.NewReader(data))
	var got []byte
	for {
		b, more, err := readChunk(src, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		if more && len(b) != 64<<10 {
			t.Fatalf("a chunk of %d bytes with more to come", len(b))
		}
		got = append(got, b...)
		if !more {
			break
		}
	}
	if !bytes.Equal(got, data) {
		t.Errorf("read %d bytes, want %d", len(got), len(data))
	}
}
