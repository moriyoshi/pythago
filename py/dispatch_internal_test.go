package py

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"testing"
)

// dispatchGlobals runs src in a new globals dict holding the given Go functions and
// returns the dict. Call it with a thread state.
func dispatchGlobals(t *testing.T, src string, fns map[string]Func) *Object {
	t.Helper()
	g, err := Eval("{}")
	if err != nil {
		t.Fatal(err)
	}
	for name, fn := range fns {
		f, err := NewFunc(name, fn)
		if err != nil {
			t.Fatal(err)
		}
		err = g.SetItem(name, f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := ExecIn(src, g); err != nil {
		t.Fatal(err)
	}
	return g
}

// goCallAllocs returns the Go heap allocations of one Python→Go call made by the Python
// function name(n), which makes n of them: the difference between n = 1000 and n = 0
// spread over the 1000 calls, so what calling name itself costs drops out.
func goCallAllocs(t *testing.T, g *Object, name string) float64 {
	t.Helper()
	fn, err := g.Item(name)
	if err != nil {
		t.Fatal(err)
	}
	defer fn.Close()
	const n = 1000
	run := func(k int) float64 {
		return testing.AllocsPerRun(20, func() {
			r, err := fn.Call(k)
			if err != nil {
				t.Fatalf("%s(%d): %v", name, k, err)
			}
			r.Close()
		})
	}
	run(n) // warm up: the argument frames' pool, the interpreter's caches
	return (run(n) - run(0)) / n
}

// A Python→Go call takes its arguments, its positional slice and its keyword names from a
// call scope, whose lanes grow to fit any call and are kept (up to arena.Keep bytes each)
// for the next: once a call of a shape has been made, later ones allocate nothing for
// their arguments, whatever their number; asgi's respond takes 8. C decodes every
// argument and keyword name (no fixed number). Keyword names come from kwNameCache: once
// a call site has made a call, its later calls copy no names, whether C decoded them
// (ASCII names) or not (non-ASCII names, a new tuple per call from f(**d)).
func TestDispatchAllocs(t *testing.T) {
	if err := Do(func() error {
		first := func(a []*Object, _ Kwargs) (any, error) { return a[0], nil }
		g := dispatchGlobals(t, `
def p1(n, x=object()):
    for _ in range(n):
        f(x)
def p8(n, x=object()):
    for _ in range(n):
        f(x, 1, 2.5, "s", b"b", None, True, x)
def p9(n, x=object()):
    for _ in range(n):
        f(x, 1, 2.5, "s", b"b", None, True, x, 9)
def k1(n, x=object()):
    for _ in range(n):
        f(x, k=x)
def k8(n, x=object()):
    for _ in range(n):
        f(x, 1, 2, 3, a=x, b=1, c=2, d=3)
def k7long(n, x=object()):
    for _ in range(n):
        f(x, timeout=x, encoding=1, errors=2, newline=3, closefd=4, opener=5, buffering=6)
def knonascii(n, x=object()):
    for _ in range(n):
        f(x, größe=x, 名前=1)
def kdict(n, x=object(), d={"timeout": 1, "encoding": 2}):
    for _ in range(n):
        f(x, **d)
def p16(n, x=object()):
    for _ in range(n):
        f(x, 1, 2.5, "s", b"b", None, True, x, 9, 10, 11, 12, 13, 14, 15, 16)
def p40(n, x=object()):
    for _ in range(n):
        f(x, *range(39))
def k8long(n, x=object()):
    for _ in range(n):
        f(x, timeout=x, encoding=1, errors=2, newline=3, closefd=4, opener=5, buffering=6, mode=7)
def k16(n, x=object()):
    for _ in range(n):
        f(x, a=1, b=2, c=3, d=4, e=5, f=6, g=7, h=8, i=9, j=10, k=11, l=12, m=13, n=14, o=15, p=16)
`, map[string]Func{"f": first})
		defer g.Close()
		for _, c := range []struct {
			name string
			want float64
		}{
			{"p1", 0},
			{"p8", 0},
			{"p9", 0},
			{"p16", 0},
			{"p40", 0}, // decoded into PyMem in C: past PYTHAGO_STACK_VALUES
			{"k8long", 0},
			{"k16", 0},
			{"k1", 0},
			{"k8", 0},
			{"k7long", 0},
			{"knonascii", 0},
			{"kdict", 0},
		} {
			got := goCallAllocs(t, g, c.name)
			if math.Abs(got-c.want) > 0.1 {
				t.Errorf("%s: a Python→Go call allocates %.2f times, want %v", c.name, got, c.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func reprOf(o *Object) string {
	s, err := o.Repr()
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return s
}

// A Func that panics gives its argument frame back like one that returns: both raise
// the same RuntimeError, so they allocate the same.
func TestDispatchPanicAllocs(t *testing.T) {
	if err := Do(func() error {
		g := dispatchGlobals(t, `
def run(f, n):
    for _ in range(n):
        try:
            f(1, 2)
        except RuntimeError:
            pass
def panics(n):
    run(panicking, n)
def fails(n):
    run(failing, n)
`, map[string]Func{
			"panicking": func([]*Object, Kwargs) (any, error) { panic("boom") },
			"failing": func([]*Object, Kwargs) (any, error) {
				return nil, panicError("boom")
			},
		})
		defer g.Close()
		failing, panicking := goCallAllocs(t, g, "fails"), goCallAllocs(t, g, "panics")
		if math.Abs(panicking-failing) > 0.1 {
			t.Errorf("a panicking Go function allocates %.2f times per call, one that returns the same error %.2f", panicking, failing)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Every active Python→Go call has arguments of its own: calls nest (Go → Python → Go
// on one thread, depth levels deep) while many goroutines make them at once, and each
// level checks before and after its nested call, and after a Release that lets other
// threads run, that it still sees the arguments Python passed it. Even levels are
// keyword calls, odd ones positional; each level returns its own argument object. The
// wide variant adds 20 positional and 6 keyword arguments to every call: past the call
// scope's inline chunks and C's stack array (decoded into PyMem).
func TestDispatchNestedConcurrent(t *testing.T) {
	t.Run("narrow", func(t *testing.T) { testDispatchNestedConcurrent(t, 0, 0) })
	t.Run("wide", func(t *testing.T) { testDispatchNestedConcurrent(t, 20, 6) })
}

func testDispatchNestedConcurrent(t *testing.T, padArgs, padKw int) {
	const (
		workers = 16
		rounds  = 30
		depth   = 6
	)
	var down *Object
	check := func(a []*Object, kw Kwargs, id, level int64, obj uintptr, when string) error {
		bad := func(what string, got any) error {
			return fmt.Errorf("call (id %d, depth %d) %s: its %s is %v: it shares its arguments with another active call", id, level, when, what, got)
		}
		wantArgs := 5 + padArgs
		if level%2 == 1 {
			wantArgs = 6 + padArgs
		}
		if len(a) != wantArgs {
			return bad("argument count", len(a))
		}
		if v, err := a[0].Int64(); err != nil || v != id {
			return bad("id", reprOf(a[0]))
		}
		if v, err := a[1].Int64(); err != nil || v != level {
			return bad("depth", reprOf(a[1]))
		}
		want := fmt.Sprintf("s%d.%d", id, level)
		if s, err := a[2].GoString(); err != nil || s != want {
			return bad("string", reprOf(a[2]))
		}
		if a[3].Ptr() != obj {
			return bad("object", reprOf(a[3]))
		}
		if v, err := a[4].Int64(); err != nil || uintptr(v) != obj {
			return bad("id(object)", reprOf(a[4]))
		}
		tag := fmt.Sprintf("t%d.%d", id, level)
		if level%2 == 0 {
			if kw.Len() != 1+padKw || kw.Get("tag") == nil {
				return bad("keywords", kw)
			}
			for j := range padKw {
				if v, err := kw.Get(fmt.Sprintf("pad%d", j)).Int64(); err != nil || v != id*100+int64(j) {
					return bad(fmt.Sprintf("keyword pad%d", j), v)
				}
			}
			if s, err := kw.Get("tag").GoString(); err != nil || s != tag {
				return bad("tag keyword", reprOf(kw.Get("tag")))
			}
		} else if s, err := a[5].GoString(); err != nil || s != tag {
			return bad("tag", reprOf(a[5]))
		}
		first := 5
		if level%2 == 1 {
			first = 6
		}
		for j := range padArgs {
			if v, err := a[first+j].Int64(); err != nil || v != id*1000+level*100+int64(j) {
				return bad(fmt.Sprintf("argument %d", first+j), reprOf(a[first+j]))
			}
		}
		return nil
	}
	rec := func(a []*Object, kw Kwargs) (any, error) {
		id, _ := a[0].Int64()
		level, _ := a[1].Int64()
		obj := a[3].Ptr()
		if err := check(a, kw, id, level, obj, "on entry"); err != nil {
			return nil, err
		}
		if level > 0 {
			r, err := down.Call(id, level-1, a[3])
			if err != nil {
				return nil, err
			}
			ok := r.Ptr() == obj
			r.Close()
			if !ok {
				return nil, fmt.Errorf("call (id %d, depth %d): its nested call returned another object", id, level)
			}
			if err := check(a, kw, id, level, obj, "after its nested call"); err != nil {
				return nil, err
			}
		}
		Release(runtime.Gosched)
		if err := check(a, kw, id, level, obj, "after Release"); err != nil {
			return nil, err
		}
		return a[3], nil
	}
	var g *Object
	if err := Do(func() error {
		g = dispatchGlobals(t, `
def down(i, d, obj):
    pad = [i * 1000 + d * 100 + j for j in range(PADARGS)]
    kw = {f"pad{j}": i * 100 + j for j in range(PADKW)}
    if d % 2:
        return rec(i, d, f"s{i}.{d}", obj, id(obj), f"t{i}.{d}", *pad)
    return rec(i, d, f"s{i}.{d}", obj, id(obj), *pad, tag=f"t{i}.{d}", **kw)
`, map[string]Func{"rec": rec})
		if err := g.SetItem("PADARGS", padArgs); err != nil {
			return err
		}
		if err := g.SetItem("PADKW", padKw); err != nil {
			return err
		}
		var err error
		down, err = g.Item("down")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer Do(func() error { down.Close(); g.Close(); return nil })
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := range workers {
		wg.Go(func() {
			for r := range rounds {
				err := Do(func() error {
					obj, err := Eval("object()")
					if err != nil {
						return err
					}
					defer obj.Close()
					res, err := down.Call(w*rounds+r, depth, obj)
					if err != nil {
						return err
					}
					defer res.Close()
					if res.Ptr() != obj.Ptr() {
						return fmt.Errorf("worker %d: the outermost call returned another object", w)
					}
					return nil
				})
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// A Python→Go call shaped like asgi's respond: eight positional arguments, of which C
// decodes the ints, the str, the bytes, None and the booleans.
func BenchmarkPythonGoCall8(b *testing.B) {
	var g, loop *Object
	if err := Do(func() error {
		var err error
		f, err := NewFunc("f", func(a []*Object, _ Kwargs) (any, error) { return a[7], nil })
		if err != nil {
			return err
		}
		defer f.Close()
		if g, err = Eval("{}"); err != nil {
			return err
		}
		if err := g.SetItem("f", f); err != nil {
			return err
		}
		if err := ExecIn(`
def loop(n, x=object()):
    for _ in range(n):
        f(1, 2, 200, b"h", 3, False, b"body", True)
`, g); err != nil {
			return err
		}
		loop, err = g.Item("loop")
		return err
	}); err != nil {
		b.Fatal(err)
	}
	defer Do(func() error { loop.Close(); g.Close(); return nil })
	b.ReportAllocs()
	if err := Do(func() error {
		b.ResetTimer()
		r, err := loop.Call(b.N)
		if err == nil {
			r.Close()
		}
		return err
	}); err != nil {
		b.Fatal(err)
	}
}
