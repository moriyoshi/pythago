package py

import (
	"fmt"
	"testing"

	"github.com/moriyoshi/pythago/internal/arena"
)

// panicsReleased runs f and describes how it did not panic with errReleased ("" if it
// did).
func panicsReleased(f func()) (msg string) {
	defer func() {
		switch v := recover().(type) {
		case nil:
		case string:
			if v != errReleased {
				msg = "panicked with " + v
			}
		default:
			msg = fmt.Sprintf("panicked with %v", v)
		}
	}()
	f()
	return "did not panic"
}

// A borrowed argument kept past its call (which a Func must not do) is poisoned when the
// call returns: its accessors panic with errReleased instead of reading freed or reused
// memory, as long as no later call has taken that memory. Both a call that fits the
// call scope's inline arrays and one that takes from its lanes are checked.
func TestBorrowedArgumentPoisoned(t *testing.T) {
	if err := Do(func() error {
		var kept [2]*Object
		keep := func(a []*Object, kw Kwargs) (any, error) {
			kept[0], kept[1] = a[0], kw.Get("k")
			return nil, nil
		}
		g := dispatchGlobals(t, `
def small():
    keep(12345, "s", k=b"kw")
def large():
    keep(12345, *range(20), k=b"kw")
`, map[string]Func{"keep": keep})
		defer g.Close()
		for _, shape := range []string{"small", "large"} {
			fn, err := g.Item(shape)
			if err != nil {
				t.Fatal(err)
			}
			r, err := fn.Call()
			fn.Close()
			if err != nil {
				t.Fatal(err)
			}
			r.Close()
			for i, o := range kept {
				if o == nil {
					t.Fatalf("%s: value %d was not kept", shape, i)
				}
				for _, use := range []struct {
					name string
					f    func()
				}{
					{"Ptr", func() { _ = o.Ptr() }},
					{"Int64", func() { _, _ = o.Int64() }},
					{"Bytes", func() { _, _ = o.Bytes() }},
					{"NewRef", func() { o.NewRef().Close() }},
					{"IsNone", func() { _ = o.IsNone() }},
					{"Go", func() { _, _ = o.Go() }},
				} {
					if msg := panicsReleased(use.f); msg != "" {
						t.Errorf("%s: value %d's %s after its call returned %s; want errReleased", shape, i, use.name, msg)
					}
				}
			}
			kept = [2]*Object{}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// With Config.CheckBorrows, a released call scope is never reused: a borrowed argument
// kept past its call panics even while later calls run, where with reuse it reads a
// later call's argument. Releases poison again every time, so only a look from inside a
// later call can tell.
func TestCheckBorrowsNeverReuses(t *testing.T) {
	old := checkBorrows
	checkBorrows = true
	defer func() { checkBorrows = old }()
	if err := Do(func() error {
		var first *Object
		var bad []string
		keep := func(a []*Object, _ Kwargs) (any, error) {
			if first == nil {
				first = a[0]
				return nil, nil
			}
			v, _ := a[0].Int64()
			if msg := panicsReleased(func() {
				if n, err := first.Int64(); err == nil {
					panic(fmt.Sprintf("Int64 read %d", n))
				}
			}); msg != "" {
				bad = append(bad, fmt.Sprintf("during call %d, the first call's argument %s", v, msg))
			}
			return nil, nil
		}
		g := dispatchGlobals(t, `
def run(v):
    keep(v, 1, 2, 3)
`, map[string]Func{"keep": keep})
		defer g.Close()
		run, err := g.Item("run")
		if err != nil {
			t.Fatal(err)
		}
		defer run.Close()
		for v := 1; v < 50; v++ {
			r, err := run.Call(v)
			if err != nil {
				t.Fatal(err)
			}
			r.Close()
		}
		if len(bad) > 0 {
			t.Errorf("%d of 48 later calls: %s; want errReleased", len(bad), bad[0])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A call scope keeps what a call of 20 arguments took for the next, and after a call
// past its lanes' bound returns to its inline arrays: one huge call does not pin its
// memory. (checkBorrows keeps release from putting the scope back into the pool, so that
// it stays this test's.)
func TestCallScopeTrims(t *testing.T) {
	old := checkBorrows
	checkBorrows = true
	defer func() { checkBorrows = old }()
	c := callScopes.New().(*callScope)
	c.objs.Take(20)
	c.bytes.Take(100)
	c.release()
	if c.objs.Cap() < 20 || c.bytes.Cap() < 100 {
		t.Errorf("after a 20-argument, 100-byte call the scope kept %d objects and %d bytes; want at least 20 and 100", c.objs.Cap(), c.bytes.Cap())
	}
	huge := arena.Keep/40 + 1 // Objects are 40 bytes
	c.objs.Take(huge)
	c.bytes.Take(arena.Keep + 1)
	c.release()
	if !c.objs.AtHome() {
		t.Errorf("after a %d-argument call the scope kept %d objects; want its inline %d", huge, c.objs.Cap(), len(c.objsIn))
	}
	if c.bytes.Cap() != 0 {
		t.Errorf("after a %d-byte call the scope kept %d bytes; want none", arena.Keep+1, c.bytes.Cap())
	}
}
