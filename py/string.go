package py

import (
	"errors"
	"sync"
)

// String owns a reference to a Python Unicode object, not a Go UTF-8 copy.
// Passing *String through an aio channel transfers ownership of that reference.
// A Go receiver must Close it or forward it; a Python receiver gets the original
// str object. Clone retains an independent reference to the same immutable text.
//
// GoString materializes a Go copy on demand. Rune-oriented access does not use
// UTF-8. Len, Rune and Runes read native fields without Python entry or cached
// metadata. Other methods enter Python as needed. Close owned strings before
// finalizing Python. A String must not be copied.
type String struct {
	mu   sync.Mutex
	view nativeUnicode
}

// NewString creates a Python Unicode object from value. It requires a running
// interpreter. Conversion from Go UTF-8 happens once here; channel transfers do
// not encode, decode or copy the text.
func NewString(value string) (*String, error) {
	s := new(String)
	err := Do(func() error {
		owner, err := From(value)
		if err != nil {
			return err
		}
		defer owner.Close()
		return owner.unicodeInto(&s.view)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Unicode retains o, which must be a str (subclasses included), in a new String.
// Call it with a thread state. The text is neither copied nor converted.
func (o *Object) Unicode() (*String, error) {
	o.live()
	s := new(String)
	if err := o.unicodeInto(&s.view); err != nil {
		return nil, err
	}
	return s, nil
}

// GoString returns an independent Go UTF-8 copy without consuming the lease.
// Python Unicode containing unpaired surrogates cannot be encoded as strict
// UTF-8; GoString returns that Python error while leaving the lease usable.
func (s *String) GoString() (string, error) {
	var value string
	err := s.withObject(func(o *nativeUnicode) error {
		var err error
		value, err = o.GoString()
		return err
	})
	return value, err
}

// Len returns the number of Unicode code points, not UTF-8 bytes or graphemes.
// Unpaired surrogates count as one code point. It does not encode the text.
func (s *String) Len() (int, error) {
	if s == nil {
		return 0, errors.New("py: nil string")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view.Len()
}

// Rune reads a native code point at a zero-based index without entering Python.
// Negative/out-of-range indexes fail; surrogate values are preserved exactly.
func (s *String) Rune(index int) (rune, error) {
	if s == nil {
		return 0, errors.New("py: nil string")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view.Rune(index)
}

// Runes copies/widens the native character array in Go without entering Python.
func (s *String) Runes() ([]rune, error) {
	if s == nil {
		return nil, errors.New("py: nil string")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view.Runes()
}

// Slice returns a new lease over the code-point range [start:end]. Bounds follow
// Go slice rules: 0 <= start <= end <= Len(). No UTF-8 representation is created.
// The substring remains valid after the source lease is closed.
func (s *String) Slice(start, end int) (*String, error) {
	out := new(String)
	err := s.withObject(func(view *nativeUnicode) error { return view.SliceInto(&out.view, start, end) })
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Clone retains another lease over the same immutable Unicode object.
func (s *String) Clone() (*String, error) {
	if s == nil {
		return nil, nil
	}
	out := new(String)
	err := s.withObject(func(view *nativeUnicode) error { return view.CloneInto(&out.view) })
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *String) withObject(fn func(*nativeUnicode) error) error {
	if s == nil {
		return errors.New("py: nil string")
	}
	return Do(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.view.IsOpen() {
			return errors.New("py: string is uninitialized, closed or transferred")
		}
		return fn(&s.view)
	})
}

// Close releases the owned reference. It is idempotent and may run on any
// goroutine. It must not race ownership transfer through a channel.
func (s *String) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if !s.view.IsOpen() {
		s.mu.Unlock()
		return nil
	}
	owner, _ := s.view.TakeObject()
	s.mu.Unlock()
	return Do(func() error { owner.Close(); return nil })
}

// TakeObject consumes the lease and returns its str object, which the caller must
// Close. A nil String gives None. Call it with a thread state.
func (s *String) TakeObject() (*Object, error) {
	if s == nil {
		return None(), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view.TakeObject()
}
