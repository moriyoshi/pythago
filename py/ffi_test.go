package py_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

// TestFuncResults covers each way a Go function's result reaches Python: a borrowed
// argument, None, booleans and nil *Object (new references taken in C), an owned
// *Object and a converted value, plus keyword arguments, errors and panics, and checks
// that repeated calls leave reference counts unchanged.
func TestFuncResults(t *testing.T) {
	fn := func(f func(a []*py.Object, kw py.Kwargs) (any, error)) py.Func { return f }
	do(t, func() error {
		setMain(t, "ret_arg", fn(func(a []*py.Object, _ py.Kwargs) (any, error) { return a[0], nil }))
		setMain(t, "ret_owned", fn(func(a []*py.Object, _ py.Kwargs) (any, error) { return a[0].NewRef(), nil }))
		setMain(t, "ret_kw", fn(func(_ []*py.Object, kw py.Kwargs) (any, error) { return kw.Get("k"), nil }))
		setMain(t, "ret_none", fn(func([]*py.Object, py.Kwargs) (any, error) { return nil, nil }))
		setMain(t, "ret_nil_object", fn(func([]*py.Object, py.Kwargs) (any, error) { return (*py.Object)(nil), nil }))
		setMain(t, "ret_true", fn(func([]*py.Object, py.Kwargs) (any, error) { return true, nil }))
		setMain(t, "ret_false", fn(func([]*py.Object, py.Kwargs) (any, error) { return false, nil }))
		setMain(t, "ret_int", fn(func([]*py.Object, py.Kwargs) (any, error) { return int64(1<<40 + 7), nil }))
		setMain(t, "ret_err", fn(func([]*py.Object, py.Kwargs) (any, error) { return nil, errors.New("boom") }))
		setMain(t, "ret_panic", fn(func([]*py.Object, py.Kwargs) (any, error) { panic("kaboom") }))
		return py.Exec(`
import sys
o = object()
assert ret_arg(o) is o and ret_owned(o) is o and ret_kw(1, k=o) is o
before = sys.getrefcount(o)
for _ in range(1000):
    ret_arg(o)
    ret_owned(o)
    ret_kw(k=o)
assert sys.getrefcount(o) == before, (before, sys.getrefcount(o))
assert ret_none() is None and ret_nil_object() is None
assert ret_true() is True and ret_false() is False
assert ret_int() == (1 << 40) + 7
for f, want in ((ret_err, "boom"), (ret_panic, "kaboom")):
    try:
        f()
    except RuntimeError as e:
        assert want in str(e), str(e)
    else:
        raise AssertionError(f"{f.__name__} did not raise")
`)
	})
}

// grow recurses through n frames of about 300 bytes each.
//
//go:noinline
func grow(n int) int {
	var pad [256]byte
	pad[n%256] = byte(n)
	if n == 0 {
		return 0
	}
	return grow(n-1) + int(pad[n%256]) - int(byte(n))
}

// TestCallbackMovesStack makes a Go function called from Python grow (and so move) the
// goroutine stack while the outer Go→C call (evaluating the expression) is in progress,
// and nests a further Go→C call there. The outer call's result is written into the
// argument frame on the goroutine stack after the move, so it is only right if the
// thunk relocates the frame (ccall_$GOARCH.s, _cgo_topofstack).
func TestCallbackMovesStack(t *testing.T) {
	do(t, func() error {
		setMain(t, "deep", py.Func(func(a []*py.Object, _ py.Kwargs) (any, error) {
			n, err := a[0].Int64()
			if err != nil {
				return nil, err
			}
			if grow(4000) != 0 { // about 1.2 MB of stack
				return nil, errors.New("grow")
			}
			inner, err := py.Eval("40 + 1") // a Go→C call on the grown stack
			if err != nil {
				return nil, err
			}
			defer inner.Close()
			m, err := inner.Int64()
			return n + m, err
		}))
		return nil
	})
	for i := range 20 {
		// A new goroutine starts on a small stack, so the first deep call moves it.
		done := make(chan any)
		go func() {
			var v any
			err := py.Do(func() error {
				o, err := py.Eval("deep(1000) + 1")
				if err != nil {
					return err
				}
				defer o.Close()
				v, err = o.Go()
				return err
			})
			if err != nil {
				v = err
			}
			done <- v
		}()
		if v := <-done; v != int64(1042) {
			t.Fatalf("run %d: got %v, want 1042", i, v)
		}
	}
}

// TestFromContainers round-trips every kind of item pythago_py_build creates in C (and
// the ones converted first), in lists, tuples and dicts.
func TestFromContainers(t *testing.T) {
	do(t, func() error {
		o, err := py.Eval("object()")
		if err != nil {
			return err
		}
		defer o.Close()
		setMain(t, "o", o)
		setMain(t, "v", []any{
			nil, true, false, int64(math.MinInt64), uint64(math.MaxUint64), -3, uint8(255),
			1.5, float32(0.25), "héllo", strings.Repeat("é", 300), "",
			[]byte("ab\x00c"), bytes.Repeat([]byte{7}, 300), []byte{},
			o, py.Tuple{1, "t", o}, map[string]any{"k": []int{1, 2}, "o": o}, []any{}, py.Tuple{},
		})
		return py.Exec(`
want = [None, True, False, -2**63, 2**64 - 1, -3, 255, 1.5, 0.25, "héllo", "é" * 300, "",
        b"ab\0c", bytes([7]) * 300, b"", o, (1, "t", o), {"k": [1, 2], "o": o}, [], ()]
assert v == want, v
assert v[15] is o and v[16][2] is o and v[17]["o"] is o
assert [type(x) for x in v] == [type(x) for x in want]
`)
	})
}

// TestFromContainersRelease checks that a container whose build fails, at the top level
// or nested, releases every reference it took, and that successful builds leave the
// items' reference counts as they were.
func TestFromContainersRelease(t *testing.T) {
	var o, unhashable *py.Object
	do(t, func() error {
		if err := py.Exec("import sys\no = object()\nunhashable = []\n"); err != nil {
			return err
		}
		o, unhashable = evalObj(t, "o"), evalObj(t, "unhashable")
		return nil
	})
	defer do(t, func() error { o.Close(); unhashable.Close(); return nil })
	before := eval(t, "sys.getrefcount(o)")
	do(t, func() error {
		for range 100 { // map order varies: the failing key comes first or last
			for _, v := range []any{
				[]any{o, "\xff", o, []any{o}},
				py.Tuple{o, []any{o, "\xff"}, o},
				map[any]any{unhashable: o, "x": []any{o}, "y": o},
				[]any{[]any{o}, []any{"\xff"}},        // converted item, then a nested failure
				py.Tuple{[]any{o}, o, make(chan int)}, // converted item, then a Go-side failure
			} {
				if r, err := py.From(v); err == nil {
					r.Close()
					return errors.New("a build that should fail succeeded")
				}
			}
			r, err := py.From([]any{o, []any{o}, map[string]any{"o": o}, py.Tuple{o}})
			if err != nil {
				return err
			}
			r.Close()
		}
		return nil
	})
	if after := eval(t, "sys.getrefcount(o)"); after != before {
		t.Fatalf("refcount of an item: %v before, %v after", before, after)
	}
}

// same reports whether two accessor results agree (NaN equals NaN).
func same(v1 any, err1 error, v2 any, err2 error) bool {
	if (err1 == nil) != (err2 == nil) {
		return false
	}
	if f1, ok := v1.(float64); ok {
		if f2, ok := v2.(float64); ok && math.IsNaN(f1) && math.IsNaN(f2) {
			return true
		}
	}
	return reflect.DeepEqual(v1, v2)
}

// TestDecodedArguments checks that every accessor answers the same from the value C
// decoded for an argument (pythago_py_fastcall) as through the C API, for each kind C
// decodes, kinds it leaves alone, arguments past the decoded ones, and keyword names.
func TestDecodedArguments(t *testing.T) {
	var mismatches []string
	var names []string
	do(t, func() error {
		setMain(t, "probe", py.Func(func(a []*py.Object, kw py.Kwargs) (any, error) {
			args := slices.Clone(a)
			names = names[:0]
			for k, v := range kw.Each {
				names = append(names, k)
				args = append(args, v)
			}
			for i, o := range args {
				ref := o.NewRef() // no decoded value: every accessor uses the C API
				defer ref.Close()
				check := func(what string, v1 any, err1 error, v2 any, err2 error) {
					if !same(v1, err1, v2, err2) {
						mismatches = append(mismatches, fmt.Sprintf("argument %d %s: decoded %v, %v; C API %v, %v", i, what, v1, err1, v2, err2))
					}
				}
				n1, e1 := o.Int64()
				n2, e2 := ref.Int64()
				check("Int64", n1, e1, n2, e2)
				f1, e1 := o.Float64()
				f2, e2 := ref.Float64()
				check("Float64", f1, e1, f2, e2)
				s1, e1 := o.GoString()
				s2, e2 := ref.GoString()
				check("GoString", s1, e1, s2, e2)
				b1, e1 := o.Bytes()
				b2, e2 := ref.Bytes()
				check("Bytes", b1, e1, b2, e2)
				u1, e1 := o.UnsafeBytes()
				u2, e2 := ref.UnsafeBytes()
				check("UnsafeBytes", u1, e1, u2, e2)
				t1, e1 := o.Truth()
				t2, e2 := ref.Truth()
				check("Truth", t1, e1, t2, e2)
				check("IsString", o.IsString(), nil, ref.IsString(), nil)
				g1, e1 := o.Go()
				g2, e2 := ref.Go()
				if o1, ok := g1.(*py.Object); ok {
					o1.Close()
					g1 = "object"
				}
				if o2, ok := g2.(*py.Object); ok {
					o2.Close()
					g2 = "object"
				}
				check("Go", g1, e1, g2, e2)
			}
			return nil, nil
		}))
		return py.Exec(`
class MyInt(int): pass
class MyStr(str): pass
values = [None, True, False, 0, -1, 2**63 - 1, -2**63, 2**63, 2**64, 1.5, -2.5, float("nan"),
          float("inf"), -0.0, 0.0, b"", b"ab\0c", "", "ascii", "h\u00e9llo", "\U0001f600",
          bytearray(b"x"), [1], MyInt(3), MyStr("s"), 7]
for i in range(0, len(values), 6):  # decoded: each call has at most 8 arguments
    probe(*values[i:i + 6])
probe(*values)  # past the decoded arguments
probe(1, b"x", k1=2, k2="v", kéy=b"y", k4=None, k5=3.5)
`)
	})
	for _, m := range mismatches {
		t.Error(m)
	}
	slices.Sort(names)
	if want := []string{"k1", "k2", "k4", "k5", "kéy"}; !slices.Equal(names, want) {
		t.Errorf("keyword names %q, want %q", names, want)
	}
}

// TestCallArguments covers Object.Call's one-call path (pythago_py_call): every kind of
// argument, more than the eight C keeps on its stack, keyword arguments, and failures in
// C and in Go, which must release every reference and return Go's own error unchanged.
func TestCallArguments(t *testing.T) {
	var echo, o *py.Object
	do(t, func() error {
		if err := py.Exec("import sys\ndef echo(*a, **k):\n    return a, k\nobj = object()\n"); err != nil {
			return err
		}
		echo, o = evalObj(t, "echo"), evalObj(t, "obj")
		return nil
	})
	defer do(t, func() error { echo.Close(); o.Close(); return nil })
	before := eval(t, "sys.getrefcount(obj)")
	do(t, func() error {
		args := []any{nil, true, int64(-5), uint64(math.MaxUint64), 2.5, "s", strings.Repeat("x", 300),
			[]byte("b"), o, []any{o, 1}, map[string]any{"o": o}}
		for range 100 {
			r, err := echo.CallKw(args, map[string]any{"k": o})
			if err != nil {
				return err
			}
			setMain(t, "r", r)
			r.Close()
			if err := py.Exec(`
a, k = r
assert a == (None, True, -5, 2**64 - 1, 2.5, "s", "x" * 300, b"b", obj, [obj, 1], {"o": obj}), a
assert a[8] is obj and k == {"k": obj}
`); err != nil {
				return err
			}
			if _, err := echo.Call(o, "\xff", []any{o}); err == nil {
				return errors.New("invalid UTF-8 accepted")
			}
			bad := make(chan int)
			if _, err := echo.Call([]any{o}, o, bad); err == nil || !strings.Contains(err.Error(), "chan int") {
				return fmt.Errorf("Go conversion error: %v", err)
			}
		}
		return py.Exec("del r, a, k")
	})
	if after := eval(t, "sys.getrefcount(obj)"); after != before {
		t.Fatalf("refcount of an argument: %v before, %v after", before, after)
	}
}

// TestSingletonResults covers calls that return None, True or False, which come back as
// shared Objects (immortal objects, no reference or cleanup of their own): they read and
// convert as usual, survive Close (twice), and pass back into Python as arguments and
// as a Go function's result.
func TestSingletonResults(t *testing.T) {
	do(t, func() error {
		f, err := py.Eval("lambda x: x")
		if err != nil {
			return err
		}
		defer f.Close()
		for _, c := range []struct {
			expr string
			want any
		}{{"None", nil}, {"True", true}, {"False", false}} {
			arg, err := py.Eval(c.expr)
			if err != nil {
				return err
			}
			r, err := f.Call(arg)
			arg.Close()
			arg.Close() // a second Close of a shared result is harmless
			if err != nil {
				return err
			}
			if v, err := r.Go(); err != nil || v != c.want {
				return fmt.Errorf("%s: Go() = %v, %v", c.expr, v, err)
			}
			if b, err := r.Truth(); err != nil || b != (c.want == true) {
				return fmt.Errorf("%s: Truth() = %v, %v", c.expr, b, err)
			}
			if r.IsNone() != (c.want == nil) {
				return fmt.Errorf("%s: IsNone() = %v", c.expr, r.IsNone())
			}
			ref := r.NewRef()
			r.Close()
			again, err := f.Call(ref) // as an argument after the result was closed
			ref.Close()
			if err != nil {
				return err
			}
			if v, _ := again.Go(); v != c.want {
				return fmt.Errorf("%s: round trip gave %v", c.expr, v)
			}
			setMain(t, "back", py.Func(func([]*py.Object, py.Kwargs) (any, error) { return again, nil }))
			if err := py.Exec("assert back() is " + c.expr + "\n"); err != nil {
				return err
			}
		}
		return nil
	})
}
