package py_test

import (
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

// Subinterpreters get the fs.FS importer through frozen modules (py/fsimport.go,
// installFrozen): every interpreter imports encodings while it is created, and the frozen
// encodings installs the path hook in that interpreter, over the Python _pythago_fs, then
// loads the real package. These tests run against testConfig: the embedded stdlib, and
// appFiles mounted as the application.

// subinterpSrc defines, in __main__, the helpers the tests run subinterpreters with:
// _sub_run(config, code) runs code in a new interpreter of that config ("isolated" or
// "legacy") with the given __main__ attributes, and raises AssertionError with the
// subinterpreter's traceback if it failed.
const subinterpSrc = `
import _interpreters, sys

def _sub_run(config, code, attrs=None):
    iid = _interpreters.create(config)
    try:
        if attrs:
            _interpreters.set___main___attrs(iid, attrs)
        exc = _interpreters.exec(iid, code)
    finally:
        _interpreters.destroy(iid)
    if exc is not None:
        raise AssertionError("%s interpreter failed:\n%s" % (config, getattr(exc, "errdisplay", None) or exc.formatted))
`

var subinterpOnce sync.Once

func subinterpSetup(t *testing.T) {
	t.Helper()
	subinterpOnce.Do(func() { do(t, func() error { return py.Exec(subinterpSrc) }) })
}

var subinterpConfigs = []string{"isolated", "legacy"}

// subinterpImportSrc imports from the embedded stdlib and from the application file
// system, as the main interpreter does: the modules come from the same files, through the
// same loader, and so does a module imported on a thread started in the subinterpreter.
func subinterpImportSrc(t *testing.T) string {
	t.Helper()
	files := map[string]string{}
	for _, m := range []string{"encodings", "json", "appmod", "appmod.helper", "encodings.cp1252"} {
		files[m] = eval(t, fmt.Sprintf("__import__('importlib').import_module(%q).__file__", m)).(string)
	}
	return fmt.Sprintf(`
import sys, importlib, importlib.resources, pkgutil, threading
import encodings, json, appmod, appmod.helper, nsapp.part
assert "café".encode("cp1252") == b"caf\xe9"  # encodings.cp1252, through the search function
want = %s
for name, file in want.items():
    m = importlib.import_module(name)
    assert m.__file__ == file, (name, m.__file__, file)
    assert type(m.__loader__).__name__ == "GoFSLoader", (name, m.__loader__)
assert sys.modules["encodings"] is encodings and encodings.search_function, encodings
assert json.loads('{"a": [1, 2]}') == {"a": [1, 2]}
assert appmod.greet("sub") == "hello sub" and appmod.helper.X == 42
assert importlib.resources.files("appmod").joinpath("data.txt").read_text() == "package data\n"
assert pkgutil.get_data("appmod", "data.txt") == b"package data\n"
assert nsapp.part.NAME == "namespace portion"
out = []
def imp():
    import json.decoder, appmod.helper
    out.append((json.decoder.__file__, appmod.helper.X))
th = threading.Thread(target=imp)
th.start()
th.join()
assert out == [(want["json"].rpartition("/")[0] + "/decoder.py", 42)], out
`, pyDict(files))
}

// pyDict renders a map of strings as a Python dict literal.
func pyDict(m map[string]string) string {
	var b strings.Builder
	b.WriteString("{")
	for k, v := range m {
		fmt.Fprintf(&b, "%q: %q, ", k, v)
	}
	b.WriteString("}")
	return b.String()
}

// Both configurations start and import from the embedded stdlib and the application
// file system, through _interpreters and, on 3.14, concurrent.interpreters.
func TestSubinterpreterImports(t *testing.T) {
	subinterpSetup(t)
	code := subinterpImportSrc(t)
	for _, config := range subinterpConfigs {
		t.Run(config, func(t *testing.T) {
			do(t, func() error {
				return py.Exec(fmt.Sprintf("_sub_run(%q, %q)", config, code))
			})
		})
	}
	t.Run("concurrent.interpreters", func(t *testing.T) {
		do(t, func() error {
			return py.Exec(fmt.Sprintf(`
import sys
if sys.version_info >= (3, 14):
    from concurrent import interpreters
    interp = interpreters.create()
    try:
        interp.exec(%q)
        assert interp.call(len, "abc") == 3
    finally:
        interp.close()
`, code))
		})
	})
}

// subinterpWalkSrc defines walk(fs), which records what fs (a _pythago_fs) says of every
// path in the tree and of a few that are not in it.
const subinterpWalkSrc = `
import binascii

def walk(fs):
    out = []
    def probe(p):
        r = []
        for f in (fs.stat, fs.read, fs.listdir):
            try:
                v = f(p)
            except Exception as e:
                v = type(e).__name__
            if isinstance(v, bytes):
                v = (len(v), binascii.crc32(v))
            r.append(v)
        out.append((p, r))
        return r
    def visit(p):
        st, _, names = probe(p)
        if st == 2:
            for n in names:
                visit(p + "/" + n)
    visit("/pythago-fs")
    for p in ("", "/", "/pythago-fs/", "/pythago-fs/app/appmod/", "/pythago-fs/app/missing",
              "/pythago-fs/app/appmod/__init__.py/x", "/pythago-fs/ap", 1, None, b"/pythago-fs"):
        probe(p)
    return out
`

// The Python _pythago_fs of a subinterpreter answers as the main interpreter's C module
// does, for every path of the tree and for paths that are not in it.
func TestSubinterpreterFSModule(t *testing.T) {
	subinterpSetup(t)
	var want []byte
	do(t, func() error {
		g, err := py.Eval("{}")
		if err != nil {
			return err
		}
		defer g.Close()
		if err := py.ExecIn(subinterpWalkSrc+"import _pythago_fs, marshal\nresult = marshal.dumps(walk(_pythago_fs))\n", g); err != nil {
			return err
		}
		r, err := g.Item("result")
		if err != nil {
			return err
		}
		defer r.Close()
		want, err = r.Bytes()
		return err
	})
	if len(want) < 10000 {
		t.Fatalf("the walk recorded only %d bytes", len(want))
	}
	code := subinterpWalkSrc + `
import _pythago_fs, marshal
assert type(_pythago_fs.stat).__name__ == "function", _pythago_fs  # the Python one
got, want = walk(_pythago_fs), marshal.loads(want)
if got != want:
    diff = [(a, b) for a, b in zip(got, want) if a != b]
    raise AssertionError("%d of %d/%d entries differ, first %r" % (len(diff), len(got), len(want), diff[:1]))
`
	for _, config := range subinterpConfigs {
		t.Run(config, func(t *testing.T) {
			do(t, func() error {
				g, err := py.Eval("{}")
				if err != nil {
					return err
				}
				defer g.Close()
				if err := g.SetItem("config", config); err != nil {
					return err
				}
				if err := g.SetItem("code", code); err != nil {
					return err
				}
				if err := g.SetItem("want", want); err != nil {
					return err
				}
				return py.ExecIn("import __main__\n__main__._sub_run(config, code, {'want': want})\n", g)
			})
		})
	}
}

// Subinterpreters are created, used and destroyed from several goroutines (through Do)
// and Python threads at once.
func TestSubinterpreterConcurrent(t *testing.T) {
	subinterpSetup(t)
	code := subinterpImportSrc(t)
	const goroutines, rounds = 6, 3
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*rounds)
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range rounds {
				config := subinterpConfigs[(i+r)%2]
				if err := py.Do(func() error { return py.Exec(fmt.Sprintf("_sub_run(%q, %q)", config, code)) }); err != nil {
					errs <- fmt.Errorf("goroutine %d round %d (%s): %w", i, r, config, err)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	do(t, func() error {
		return py.Exec(fmt.Sprintf(`
import threading
errors = []
def work(i):
    try:
        for r in range(3):
            _sub_run(("isolated", "legacy")[(i + r) %% 2], %q)
    except BaseException as e:
        errors.append(e)
threads = [threading.Thread(target=work, args=(i,)) for i in range(6)]
for th in threads:
    th.start()
for th in threads:
    th.join()
assert not errors, errors
`, code))
	})
}

// Interpreters are destroyed and created again, one after another, each starting afresh.
func TestSubinterpreterRecreate(t *testing.T) {
	subinterpSetup(t)
	do(t, func() error {
		return py.Exec(`
import _interpreters
before = len(_interpreters.list_all())
ids = set()
for i in range(12):
    config = ("isolated", "legacy")[i % 2]
    iid = _interpreters.create(config)
    assert iid not in ids, (iid, ids)
    ids.add(iid)
    exc = _interpreters.exec(iid, """
import sys
assert "appmod" not in sys.modules and "json" not in sys.modules, sorted(sys.modules)
import json, appmod
assert appmod.greet("again") == "hello again"
""")
    _interpreters.destroy(iid)
    assert exc is None, (config, exc.formatted)
assert len(_interpreters.list_all()) == before, _interpreters.list_all()
`)
	})
}

// A forked child, where Go cannot run, creates subinterpreters too: the importer is all C
// and Python.
func TestSubinterpreterForkChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no fork on Windows")
	}
	subinterpSetup(t)
	do(t, func() error {
		return py.Exec(`
import os, warnings
warnings.simplefilter("ignore", DeprecationWarning)  # fork() of a multi-threaded process
r, w = os.pipe()
pid = os.fork()
if pid == 0:
    try:
        for config in ("isolated", "legacy"):
            _sub_run(config, "import json, appmod.helper\nassert appmod.helper.X == 42 and json.dumps(1) == '1'")
        os.write(w, b"ok")
    except BaseException as e:
        os.write(w, repr(e).encode())
    os._exit(0)
os.close(w)
with os.fdopen(r, "rb") as f:
    msg = f.read()
os.waitpid(pid, 0)
assert msg == b"ok", msg
`)
	})
}

// With the stdlib on disk (PYTHONHOME), the application file system goes first on
// sys.path as before, and subinterpreters import from both. The child is a sys.executable
// process with testConfig, whose stdlib is a copy of the embedded one.
func TestSubinterpreterDiskStdlib(t *testing.T) {
	if testing.Short() {
		t.Skip("copies the stdlib")
	}
	f, _ := eval(t, "__import__('json').__file__").(string)
	src := path.Dir(path.Dir(f))
	home := t.TempDir()
	lib := filepath.Join(home, "lib", path.Base(src))
	if runtime.GOOS == "windows" {
		lib = filepath.Join(home, "Lib")
	}
	do(t, func() error {
		return py.Exec(fmt.Sprintf(`
import os, _pythago_fs as fs
def copy(s, d):
    os.makedirs(d, exist_ok=True)
    for n in fs.listdir(s):
        if fs.stat(s + "/" + n) == 2:
            copy(s + "/" + n, os.path.join(d, n))
        else:
            with open(os.path.join(d, n), "wb") as f:
                f.write(fs.read(s + "/" + n))
copy(%q, %q)
`, src, lib))
	})
	script := fmt.Sprintf(`
import os, sys, json, appmod
lib = os.path.normcase(%q)
def ondisk(m):
    return os.path.normcase(m.__file__).startswith(lib)
assert ondisk(json), json.__file__
assert sys.path.count("/pythago-fs/app") == 1 and sys.path.index("/pythago-fs/app") <= 1, sys.path
assert appmod.__file__ == "/pythago-fs/app/appmod/__init__.py", appmod.__file__
import _interpreters
for config in ("isolated", "legacy"):
    iid = _interpreters.create(config)
    _interpreters.set___main___attrs(iid, {"lib": lib})
    exc = _interpreters.exec(iid, """
import os, sys, encodings, json, appmod
for m in (encodings, json):
    assert os.path.normcase(m.__file__).startswith(lib), m.__file__
# On Windows, site.py made the entries absolute (Z:\\pythago-fs\\app), as in the main
# interpreter before Init puts them back.
assert [p.replace(chr(92), "/").split(":", 1)[-1] for p in sys.path].count("/pythago-fs/app") == 1, sys.path
assert appmod.__file__ == "/pythago-fs/app/appmod/__init__.py" and appmod.greet("disk") == "hello disk"
""")
    _interpreters.destroy(iid)
    assert exc is None, (config, exc.formatted)
print("disk stdlib ok")
`, lib)
	do(t, func() error {
		return py.Exec(fmt.Sprintf(`
import os, subprocess, sys
r = subprocess.run([sys.executable, "-c", %q], capture_output=True, text=True, timeout=120,
                   env=dict(os.environ, PYTHONHOME=%q))
assert r.returncode == 0 and "disk stdlib ok" in r.stdout, (r.returncode, r.stdout, r.stderr)
`, script, home))
	})
}
