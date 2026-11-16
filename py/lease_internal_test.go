package py

import (
	"bytes"
	"testing"
)

// scribbleLease is a lease whose release overwrites its memory, as a lender reusing it
// would, and counts releases.
type scribbleLease struct {
	b        []byte
	released int
}

func (l *scribbleLease) LeasedBytes() []byte { return l.b }
func (l *scribbleLease) ReleaseLease() {
	l.released++
	for i := range l.b {
		l.b[i] = 'X'
	}
}

// A lease converted by From (here inside a tuple, as a posted value is) becomes bytes
// with the memory's contents at conversion: the copy is made before the lease is
// released, which happens exactly once.
func TestLeaseCopiedThenReleased(t *testing.T) {
	if err := Do(func() error {
		l := &scribbleLease{b: []byte("lent body bytes")}
		o, err := From(Tuple{1, l})
		if err != nil {
			return err
		}
		defer o.Close()
		item, err := o.Item(1)
		if err != nil {
			return err
		}
		defer item.Close()
		got, err := item.Bytes()
		if err != nil {
			return err
		}
		if !bytes.Equal(got, []byte("lent body bytes")) {
			t.Errorf("the bytes object holds %q: the lease was released before the copy", got)
		}
		if l.released != 1 {
			t.Errorf("released %d times, want 1", l.released)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
