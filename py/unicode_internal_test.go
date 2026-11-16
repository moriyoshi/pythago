package py

import (
	"errors"
	"slices"
	"testing"
)

// The Object Unicode accessors read a str natively, as String does, and use the C
// API only for what it alone can do: raise IndexError or TypeError.
func TestUnicodeAccessorsReadStrNatively(t *testing.T) {
	if nativeUnicodeDefinition == nil {
		t.Skip(nativeUnicodeUnavailable)
	}
	var calls []string
	lenAPI, runeAPI, runesAPI := unicodeLenAPI, unicodeRuneAPI, unicodeRunesAPI
	unicodeLenAPI = func(o *Object) (int, error) { calls = append(calls, "len"); return unicodeLenC(o) }
	unicodeRuneAPI = func(o *Object, i int) (rune, error) { calls = append(calls, "rune"); return unicodeRuneC(o, i) }
	unicodeRunesAPI = func(o *Object) ([]rune, error) { calls = append(calls, "runes"); return unicodeRunesC(o) }
	defer func() { unicodeLenAPI, unicodeRuneAPI, unicodeRunesAPI = lenAPI, runeAPI, runesAPI }()

	err := Do(func() error {
		for _, src := range []string{
			`"ASCII"`, `"café"`, `"日本語\x00"`, `"A🤖\ud800𐍈"`, `""`,
			// Noncompact subclass storage; the overrides must not run.
			`type("T", (str,), {"__len__": lambda s: 99, "__getitem__": lambda s, i: "x"})("é🤖")`,
		} {
			o, err := Eval(src)
			if err != nil {
				return err
			}
			want, err := unicodeRunesC(o)
			if err != nil {
				return err
			}
			calls = nil
			n, err := o.UnicodeLen()
			if err != nil || n != len(want) {
				t.Errorf("%s: UnicodeLen = %d, %v; want %d", src, n, err, len(want))
			}
			got, err := o.UnicodeRunes()
			if err != nil || !slices.Equal(got, want) {
				t.Errorf("%s: UnicodeRunes = %q, %v; want %q", src, got, err, want)
			}
			for i, r := range want {
				if got, err := o.UnicodeRune(i); err != nil || got != r {
					t.Errorf("%s: UnicodeRune(%d) = %U, %v; want %U", src, i, got, err, r)
				}
			}
			if calls != nil {
				t.Errorf("%s: str reads called the C API: %v", src, calls)
			}
			// Out of range still raises IndexError through the C API.
			for _, i := range []int{-1, len(want)} {
				var pe *Error
				if _, err := o.UnicodeRune(i); !errors.As(err, &pe) || pe.Type != "IndexError" {
					t.Errorf("%s: UnicodeRune(%d) = %v; want IndexError", src, i, err)
				}
			}
			o.Close()
		}
		// A non-str goes to the C API, which raises.
		o, err := From([]byte("bytes"))
		if err != nil {
			return err
		}
		defer o.Close()
		calls = nil
		if _, err := o.UnicodeLen(); err == nil {
			t.Error("UnicodeLen of bytes succeeded")
		}
		if _, err := o.UnicodeRunes(); err == nil {
			t.Error("UnicodeRunes of bytes succeeded")
		}
		if !slices.Equal(calls, []string{"len", "runes"}) {
			t.Errorf("bytes went to %v; want the C API", calls)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func BenchmarkUnicodeAccessors(b *testing.B) {
	var o *Object
	if err := Do(func() error { var err error; o, err = From("Aé🤖日本語"); return err }); err != nil {
		b.Fatal(err)
	}
	defer Do(func() error { o.Close(); return nil })
	for _, c := range []struct {
		name string
		fn   func() error
	}{
		{"Len/Native", func() error { _, err := o.UnicodeLen(); return err }},
		{"Len/CAPI", func() error { _, err := unicodeLenC(o); return err }},
		{"Rune/Native", func() error { _, err := o.UnicodeRune(2); return err }},
		{"Rune/CAPI", func() error { _, err := unicodeRuneC(o, 2); return err }},
		{"Runes/Native", func() error { _, err := o.UnicodeRunes(); return err }},
		{"Runes/CAPI", func() error { _, err := unicodeRunesC(o); return err }},
	} {
		b.Run(c.name, func(b *testing.B) {
			if err := Do(func() error {
				b.ReportAllocs()
				for range b.N {
					if err := c.fn(); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
		})
	}
}
