package py

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// kwProbe returns its keyword arguments as Python sees them, [(name, value), ...], read
// through every Kwargs accessor, and fails if the accessors disagree.
func kwProbe(_ []*Object, kw Kwargs) (any, error) {
	items := make([]any, 0, kw.Len())
	m := kw.Map()
	if len(m) != kw.Len() {
		return nil, fmt.Errorf("Map has %d entries, Len is %d", len(m), kw.Len())
	}
	i := 0
	for name, v := range kw.Each {
		if name != kw.Name(i) || v != kw.Value(i) {
			return nil, fmt.Errorf("Each yields (%q, %p) at %d, Name and Value (%q, %p)", name, v, i, kw.Name(i), kw.Value(i))
		}
		if kw.Get(name) != v || m[name] != v {
			return nil, fmt.Errorf("keyword %q: Get %p, Map %p, Value %p", name, kw.Get(name), m[name], v)
		}
		items = append(items, Tuple{name, v})
		i++
	}
	if i != kw.Len() {
		return nil, fmt.Errorf("Each yields %d arguments, Len is %d", i, kw.Len())
	}
	if kw.Get("not passed") != nil {
		return nil, errors.New(`Get("not passed") is not nil`)
	}
	return items, nil
}

// A Go function sees the keyword arguments a Python function would, in the same order,
// for each way names reach Go: decoded in C (ASCII names) or not (non-ASCII names), from
// a call site's constant tuple or a new one per call (f(**d)), past the call scope's
// inline chunks (8 arguments), and many distinct names built at run time, which evict
// each other from kwNameCache.
func TestKwargsMatchesPython(t *testing.T) {
	if err := Do(func() error {
		g := dispatchGlobals(t, `
def ref(*a, **kw):
    return list(kw.items())
def run():
    calls = [
        ((), {}),
        ((1,), {}),
        ((1,), {"k": 2}),
        ((), {"a": 1, "b": "s", "c": 2.5, "d": None}),
        ((1, 2), {"timeout": 1, "encoding": "utf-8", "errors": "strict", "newline": "",
                  "closefd": True, "opener": None}),
        ((1,), {"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8}),
        ((), {"größe": 1, "名前": 2, "k": 3, "😀": 4, "x": 5}),
    ]
    calls += [((), {"n%d" % i: i, "m%d" % (i * 7): i}) for i in range(600)]
    bad = []
    for _ in range(2):
        for a, kw in calls:
            want, got = ref(*a, **kw), probe(*a, **kw)
            if got != want:
                bad.append((a, kw, got))
    # The same names through each call site's constant tuple.
    for _ in range(2):
        for got, want in [
            (probe(k=2), [("k", 2)]),
            (probe(1, a=1, b=2, c=3, d=4, e=5, f=6, g=7), [("a", 1), ("b", 2), ("c", 3), ("d", 4), ("e", 5), ("f", 6), ("g", 7)]),
            (probe(größe=1, k=2), [("größe", 1), ("k", 2)]),
        ]:
            if got != want:
                bad.append((want, got))
    return bad[:5]
`, map[string]Func{"probe": kwProbe})
		defer g.Close()
		run, err := g.Item("run")
		if err != nil {
			return err
		}
		defer run.Close()
		r, err := run.Call()
		if err != nil {
			return err
		}
		defer r.Close()
		if n, _ := r.Len(); n != 0 {
			t.Errorf("Go saw other keyword arguments than Python: %s", reprOf(r))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// kwChurnSrc makes calls whose names (and name tuples) are new objects each time and die
// after the call, so that the next call's are likely to reuse their memory: a cache keyed
// by an address it does not keep alive would hand a call the previous call's name.
// Each call passes its name decoded in C (position 0, ASCII; position 4, past what the glue
// decoded before ABI 4) or not decodable in C (non-ASCII).
const kwChurnSrc = `
def churn(n, prefix):
    bad = []
    for i in range(n):
        name = "%s%d" % (prefix, i)
        for got, want in [
            (names(**{name: i}), [name]),
            (names(a=0, b=0, c=0, d=0, **{name: i}), ["a", "b", "c", "d", name]),
            (names(**{"é" + name: i}), ["é" + name]),
        ]:
            if got != want:
                bad.append((want, got))
    return bad[:3]
`

func kwNamesOf(_ []*Object, kw Kwargs) (any, error) {
	out := make([]any, kw.Len())
	for i := range out {
		out[i] = kw.Name(i)
	}
	return out, nil
}

// A cached keyword name is never handed to a call whose name lives where a dead one did:
// kwNameCache picks a slot by address but uses an entry only for a name equal to it.
func TestKwargsNamesStayTheirs(t *testing.T) {
	var churn *Object
	var g *Object
	if err := Do(func() error {
		g = dispatchGlobals(t, kwChurnSrc, map[string]Func{"names": kwNamesOf})
		var err error
		churn, err = g.Item("churn")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer Do(func() error { churn.Close(); g.Close(); return nil })
	// Many threads at once (3.14t runs them in parallel), each with names of its own.
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := range workers {
		wg.Go(func() {
			errs <- Do(func() error {
				r, err := churn.Call(30000, fmt.Sprintf("w%d_", w))
				if err != nil {
					return err
				}
				defer r.Close()
				if n, _ := r.Len(); n != 0 {
					return fmt.Errorf("worker %d: calls were handed other calls' keyword names (want, got): %s", w, reprOf(r))
				}
				return nil
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

// A keyword name Go cannot read (a lone surrogate has no UTF-8 form) raises before the
// Go function runs, as with the map API, and leaves the caches usable.
func TestKwargsUndecodableName(t *testing.T) {
	called := 0
	if err := Do(func() error {
		g := dispatchGlobals(t, `
def run():
    errs = 0
    for _ in range(50):
        try:
            names(**{"\ud800": 1})
        except UnicodeEncodeError:
            errs += 1
    return errs, names(**{"ok": 1}), names(ok=1)
`, map[string]Func{"names": func(a []*Object, kw Kwargs) (any, error) {
			called++
			return kwNamesOf(a, kw)
		}})
		defer g.Close()
		run, err := g.Item("run")
		if err != nil {
			return err
		}
		defer run.Close()
		r, err := run.Call()
		if err != nil {
			return err
		}
		defer r.Close()
		if s := reprOf(r); s != "(50, ['ok'], ['ok'])" || called != 2 {
			t.Errorf("run() = %s with %d calls of the Go function, want (50, ['ok'], ['ok']) with 2", s, called)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
