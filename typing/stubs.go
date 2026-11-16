// Package typing holds the type stubs of the types-pythago distribution (this directory's
// pyproject.toml) for the tests of aio, asgi and wsgi, which check them against the live
// modules with stubcheck.py (Check).
package typing

import (
	"embed"
	"fmt"

	"github.com/moriyoshi/pythago/py"
)

//go:embed stubcheck.py all:goaio-stubs all:goaio_tls-stubs all:_goaio-stubs
//go:embed all:pythago_asgi-stubs all:_pythago_asgi-stubs all:pythago_wsgi-stubs all:_pythago_wsgi-stubs
var files embed.FS

// Stub returns the stub of module name.
func Stub(name string) (string, error) {
	b, err := files.ReadFile(name + "-stubs/__init__.pyi")
	return string(b), err
}

// Check compares the stub of each module with the module (stubcheck.py): it returns the
// mismatches, one per line. Attached.
func Check(modules ...string) ([]string, error) {
	src, err := files.ReadFile("stubcheck.py")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range modules {
		stub, err := Stub(name)
		if err != nil {
			return nil, err
		}
		ns, err := py.From(map[string]any{"__name__": "_stubcheck", "_module": name, "_stub": stub})
		if err != nil {
			return nil, err
		}
		err = py.ExecIn(string(src)+"\n_problems = check(_module, _stub)\n", ns)
		if err == nil {
			var o *py.Object
			if o, err = ns.Item("_problems"); err == nil {
				var v any
				if v, err = o.Go(); err == nil {
					for _, p := range v.([]any) {
						out = append(out, fmt.Sprint(p))
					}
				}
				o.Close()
			}
		}
		ns.Close()
		if err != nil {
			return nil, fmt.Errorf("stubcheck %s: %w", name, err)
		}
	}
	return out, nil
}
