package py_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

func TestStringRuneAccess(t *testing.T) {
	for _, text := range []string{"", "ASCII", "café", "日本語", "𐍈🤖", "e\u0301", "a\x00b"} {
		t.Run(fmt.Sprintf("%q", text), func(t *testing.T) {
			value, err := py.NewString(text)
			if err != nil {
				t.Fatal(err)
			}
			defer value.Close()
			want := []rune(text)
			n, err := value.Len()
			if err != nil || n != len(want) {
				t.Fatalf("Len = %d, %v; want %d", n, err, len(want))
			}
			all, err := value.Runes()
			if err != nil || !slices.Equal(all, want) {
				t.Fatalf("Runes = %v, %v; want %v", all, err, want)
			}
			for i, expected := range want {
				r, err := value.Rune(i)
				if err != nil || r != expected {
					t.Fatalf("Rune(%d) = %U, %v; want %U", i, r, err, expected)
				}
			}
			for _, i := range []int{-1, n, n + 1, int(^uint(0) >> 1)} {
				if _, err := value.Rune(i); err == nil {
					t.Fatalf("Rune(%d) accepted", i)
				}
			}
			for _, bounds := range [][2]int{{0, 0}, {0, n}, {n, n}} {
				part, err := value.Slice(bounds[0], bounds[1])
				if err != nil {
					t.Fatal(err)
				}
				got, err := part.GoString()
				part.Close()
				if err != nil || got != string(want[bounds[0]:bounds[1]]) {
					t.Fatalf("Slice %v = %q, %v", bounds, got, err)
				}
			}
			for _, bounds := range [][2]int{{-1, n}, {0, n + 1}, {1, 0}} {
				if part, err := value.Slice(bounds[0], bounds[1]); err == nil {
					part.Close()
					t.Fatalf("Slice %v accepted", bounds)
				}
			}
			if n > 0 {
				all[0] = 'X'
				r, _ := value.Rune(0)
				if r != want[0] {
					t.Fatal("Runes aliases the Unicode storage")
				}
			}
			value.Close()
			if _, err := value.Len(); err == nil {
				t.Fatal("closed Len succeeded")
			}
			if _, err := value.Rune(0); err == nil {
				t.Fatal("closed Rune succeeded")
			}
			if _, err := value.Runes(); err == nil {
				t.Fatal("closed Runes succeeded")
			}
			if part, err := value.Slice(0, 0); err == nil {
				part.Close()
				t.Fatal("closed Slice succeeded")
			}
		})
	}
}
