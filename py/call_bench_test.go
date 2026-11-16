package py

import "testing"

// The C calls behind the Unicode accessors, Attr and Import use the fixed-arity call
// helpers (call0 … call8, callStr0, callStr1) or a cframe of their own; a variadic
// uintptrescapes call heap-allocates its argument array on every call. What remains is
// the allocation each operation needs: an owned *Object (the Object and its GC cleanup,
// measured as newRef), the rune slice, the C string.
func TestCCallSitesAllocateOnlyTheirResults(t *testing.T) {
	if err := Do(func() error {
		o, err := From("Aé🤖日本語")
		if err != nil {
			return err
		}
		defer o.Close()
		owned := testing.AllocsPerRun(200, func() { newRef(o.p).Close() })
		if owned == 0 {
			t.Fatal("an owned *Object allocates nothing; the expectations below assume it does")
		}
		for _, c := range []struct {
			name string
			want float64
			fn   func()
		}{
			{"unicodeLenC", 0, func() {
				if n, err := unicodeLenC(o); err != nil || n != 6 {
					t.Fatalf("unicodeLenC = %d, %v", n, err)
				}
			}},
			{"unicodeRuneC", 0, func() {
				if r, err := unicodeRuneC(o, 2); err != nil || r != '🤖' {
					t.Fatalf("unicodeRuneC = %q, %v", r, err)
				}
			}},
			{"unicodeRunesC", 1, func() { // the []rune
				if _, err := unicodeRunesC(o); err != nil {
					t.Fatal(err)
				}
			}},
			{"UnicodeSlice", owned, func() { // the result
				s, err := o.UnicodeSlice(1, 3)
				if err != nil {
					t.Fatal(err)
				}
				s.Close()
			}},
			{"SliceInto", 2 * owned, func() { // the view's owner and the result
				var u, out nativeUnicode
				if err := o.unicodeInto(&u); err != nil {
					t.Fatal(err)
				}
				if err := u.SliceInto(&out, 1, 3); err != nil {
					t.Fatal(err)
				}
				out.Close()
				u.Close()
			}},
			{"Attr", owned + 1, func() { // the result and the C string (callStr1)
				m, err := o.Attr("upper")
				if err != nil {
					t.Fatal(err)
				}
				m.Close()
			}},
			{"Import", owned + 1, func() { // the result and the C string (callStr0)
				m, err := Import("sys")
				if err != nil {
					t.Fatal(err)
				}
				m.Close()
			}},
		} {
			if c.name == "SliceInto" && nativeUnicodeDefinition == nil {
				continue
			}
			if got := testing.AllocsPerRun(200, c.fn); got != c.want {
				t.Errorf("%s: %v allocations per call, want %v", c.name, got, c.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A Go function called from Python, positionally and with a keyword: the Python loop
// makes b.N calls of the builtin.
func BenchmarkPythonGoCall(b *testing.B) {
	var echo, positional, keyword *Object
	if err := Do(func() error {
		var err error
		echo, err = NewFunc("echo", func(a []*Object, _ Kwargs) (any, error) { return a[0], nil })
		if err != nil {
			return err
		}
		g, err := Eval("{}")
		if err != nil {
			return err
		}
		defer g.Close()
		if err := g.SetItem("echo", echo); err != nil {
			return err
		}
		if err := ExecIn(`
def positional(n, x=object()):
    for _ in range(n):
        echo(x)
def keyword(n, x=object()):
    for _ in range(n):
        echo(x, k=x)
`, g); err != nil {
			return err
		}
		if positional, err = g.Item("positional"); err != nil {
			return err
		}
		keyword, err = g.Item("keyword")
		return err
	}); err != nil {
		b.Fatal(err)
	}
	defer Do(func() error { positional.Close(); keyword.Close(); echo.Close(); return nil })
	for _, c := range []struct {
		name string
		fn   *Object
	}{{"Positional", positional}, {"Keyword", keyword}} {
		b.Run(c.name, func(b *testing.B) {
			if err := Do(func() error {
				b.ResetTimer()
				r, err := c.fn.Call(b.N)
				if err == nil {
					r.Close()
				}
				return err
			}); err != nil {
				b.Fatal(err)
			}
		})
	}
}

// Attr and Import pass their name to C as a C string: getattr on a module, and import of
// a module already in sys.modules.
func BenchmarkStrCall(b *testing.B) {
	for _, c := range []struct {
		name string
		fn   func(*Object) (*Object, error)
	}{
		{"Attr", func(m *Object) (*Object, error) { return m.Attr("path") }},
		{"Import", func(*Object) (*Object, error) { return Import("sys") }},
	} {
		b.Run(c.name, func(b *testing.B) {
			if err := Do(func() error {
				m, err := Import("sys")
				if err != nil {
					return err
				}
				defer m.Close()
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					r, err := c.fn(m)
					if err != nil {
						return err
					}
					r.Close()
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
		})
	}
}
