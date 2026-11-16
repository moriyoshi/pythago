package py_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/moriyoshi/pythago/py"
)

// dyldImages sets names to the paths of the images dyld has loaded, in load order.
const dyldImages = `
import ctypes
libc = ctypes.CDLL(None)
libc._dyld_get_image_name.restype = ctypes.c_char_p
libc._dyld_get_image_name.argtypes = [ctypes.c_uint32]
names = [libc._dyld_get_image_name(i).decode() for i in range(libc._dyld_image_count())]
`

// Config.ExtensionShimDir chooses where the shim's file is written; there is no memfd on
// macOS, so Config.ExtensionShimMemfd fails Init. Each case runs in a process started from
// sys.executable with testConfig's environment variables; the child prints the shim's
// image name after checking that the C API resolves from the global scope.
func TestExtensionShimSource(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import os, re, subprocess, sys, tempfile
probe = ` + "'''" + `
import ctypes
# First, as dyldImages looks up only names outside the C API, which do not load the shim.
# The C API is in the global scope; after a failed load, the AttributeError's cause
# (in the traceback) is the load's error.
ctypes.CDLL(None).PyLong_FromLong
` + dyldImages + `
shim = [n for n in names if n.endswith("/libpython-pythago.dylib")]
assert len(shim) == 1, names
print(shim[0])
` + "'''" + `
def child(**env):
    e = {k: v for k, v in os.environ.items() if not k.startswith("PYTHAGO_TEST_EXTSHIM_")}
    e.update(env)
    return subprocess.run([sys.executable, "-c", probe], capture_output=True, text=True,
                          timeout=60, env=e)

def check(r, parent):
    assert r.returncode == 0, r.stderr
    name = r.stdout.strip()
    # dyld names the real path (/private/var/folders/..., not /var/folders/...).
    assert re.fullmatch(re.escape(os.path.realpath(parent)) + r"/pythago-shim-[^/]+/libpython-pythago\.dylib", name), (name, parent)
    assert not os.path.exists(os.path.dirname(name)), f"{name}'s directory was left behind"

with tempfile.TemporaryDirectory() as d:
    check(child(PYTHAGO_TEST_EXTSHIM_DIR=d), d)
    assert os.listdir(d) == [], os.listdir(d)

# The default is os.TempDir, which is $TMPDIR as for tempfile.
with tempfile.TemporaryDirectory() as d:
    check(child(TMPDIR=d), d)

r = child(PYTHAGO_TEST_EXTSHIM_MEMFD="1")
assert r.returncode != 0 and "extension shim" in r.stderr and "memfd" in r.stderr, (r.returncode, r.stdout, r.stderr)

# A directory that cannot be used is an error.
with tempfile.TemporaryDirectory() as d:
    r = child(PYTHAGO_TEST_EXTSHIM_DIR=os.path.join(d, "missing"))
assert r.returncode != 0 and "extension shim" in r.stderr and "missing" in r.stderr, (r.returncode, r.stdout, r.stderr)
`)
	})
}

// Only the interpreter's C API (^_?Py) is in the global scope: not the glue's pythago_*
// functions, which the image also lists, nor PY_TIMEOUT_MAX, libpython's one export
// outside the rule (no extension in the tested wheels binds it).
func TestExtensionShimExports(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import ctypes
g = ctypes.CDLL(None)
# Each of the last three has another export as a prefix (PyLong_FromLong, ...), which a
# trie that dyld's first-match walk misreads hides.
for name in ("PyLong_FromLong", "_Py_NoneStruct", "PyExc_SystemError", "_PyRuntime", "Py_IsInitialized",
             "PyLong_FromLongLong", "PyObject_CallMethod", "Py_InitializeEx"):
    assert hasattr(g, name), f"{name} is not in the global scope"
for name in ("pythago_py_abi", "pythago_py_call", "pythago_symtab", "PY_TIMEOUT_MAX"):
    assert not hasattr(g, name), f"{name} is in the global scope"
`)
	})
}

// An extension module's references to the C API are flat-namespace binds (wheels link
// with -undefined dynamic_lookup): a bundle built that way, loaded RTLD_LOCAL as the
// import system loads extensions, binds functions through its lazy pointers and data
// objects through its GOT to the image's C API. Needs a C compiler (Xcode's command line
// tools); there are no Python headers here, so the bundle declares what it uses.
func TestExtensionShimFlatNamespace(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler to build a flat-namespace bundle with")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "probe.c")
	bundle := filepath.Join(dir, "probe.so")
	if err := os.WriteFile(src, []byte(`
typedef struct _object PyObject;
extern PyObject *PyLong_FromLong(long);
extern PyObject *PyUnicode_FromString(const char *);
extern int Py_IsInitialized(void);
extern PyObject *PyExc_SystemError;
extern char _Py_NoneStruct;
PyObject *probe_long(long v) { return PyLong_FromLong(v); }
PyObject *probe_str(void) { return PyUnicode_FromString("from a bundle"); }
int probe_initialized(void) { return Py_IsInitialized(); }
PyObject *probe_exc(void) { return PyExc_SystemError; }
void *probe_none(void) { return &_Py_NoneStruct; }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(cc, "-bundle", "-undefined", "dynamic_lookup", "-o", bundle, src).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, out)
	}
	do(t, func() error {
		return py.Exec(fmt.Sprintf(`
import ctypes
lib = ctypes.PyDLL(%q)
lib.probe_long.restype = ctypes.py_object
lib.probe_long.argtypes = [ctypes.c_long]
lib.probe_str.restype = ctypes.py_object
lib.probe_exc.restype = ctypes.py_object
lib.probe_none.restype = ctypes.c_void_p
assert lib.probe_initialized() == 1
assert lib.probe_long(12345678901) == 12345678901
assert lib.probe_str() == "from a bundle"
assert lib.probe_exc() is SystemError, lib.probe_exc()
assert lib.probe_none() == id(None)
`, bundle))
	})
}

// The lazy-load tests run their script with py.Main in a new process of this binary,
// configured by lazyShimConfig (the environment variable reaches its sys.executable
// children too): the test process itself loaded the shim long ago.
const lazyShimEnv = "PYTHAGO_TEST_LAZYSHIM"

func init() {
	if os.Getenv(lazyShimEnv) != "" {
		os.Exit(py.Main(lazyShimConfig(), os.Args))
	}
}

func lazyShimConfig() py.Config {
	cfg := py.Config{
		NoExtensions:     os.Getenv(lazyShimEnv) == "noext",
		ExtensionShimDir: os.Getenv("PYTHAGO_TEST_LAZYSHIM_DIR"),
		Modules: map[string]map[string]any{
			"_shimprobe": {"shims": py.Func(func([]*py.Object, py.Kwargs) (any, error) {
				return loadedShims(), nil
			})},
		},
	}
	if p := os.Getenv("PYTHAGO_TEST_LAZYSHIM_PATH"); p != "" {
		cfg.SearchPaths = filepath.SplitList(p)
	}
	return cfg
}

var (
	dyldOnce      sync.Once
	dyldCount     func() uint32
	dyldImageName func(uint32) string
)

// loadedShims lists the extension shims in dyld's image list, read from Go: listing it
// from Python (ctypes) would load the shim.
func loadedShims() []string {
	dyldOnce.Do(func() {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		purego.RegisterLibFunc(&dyldCount, lib, "_dyld_image_count")
		purego.RegisterLibFunc(&dyldImageName, lib, "_dyld_get_image_name")
	})
	shims := []string{}
	for i := range dyldCount() {
		if name := dyldImageName(i); strings.HasSuffix(name, "/libpython-pythago.dylib") {
			shims = append(shims, name)
		}
	}
	return shims
}

// probeExtensions builds n extension modules lazyprobe0..lazyprobe<n-1> in a new
// directory: copies of one flat-namespace bundle whose PyInit_lazyprobeN raises
// RuntimeError("probe lazyprobeN initialized"), which needs PyErr_SetString and
// PyExc_RuntimeError bound (no Python headers here, so no module object). Each copy is
// a file of its own, so each import is a dlopen of its own.
func probeExtensions(t *testing.T, n int) (dir string) {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler to build a flat-namespace bundle with")
	}
	dir = t.TempDir()
	var src strings.Builder
	src.WriteString("typedef struct _object PyObject;\nextern PyObject *PyExc_RuntimeError;\n" +
		"extern void PyErr_SetString(PyObject *, const char *);\n")
	for i := range n {
		fmt.Fprintf(&src, "PyObject *PyInit_lazyprobe%d(void) { PyErr_SetString(PyExc_RuntimeError, \"probe lazyprobe%d initialized\"); return 0; }\n", i, i)
	}
	c := filepath.Join(dir, "probe.c")
	if err := os.WriteFile(c, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "lazyprobe0.so")
	if out, err := exec.Command(cc, "-bundle", "-undefined", "dynamic_lookup", "-o", bundle, c).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, out)
	}
	b, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("lazyprobe%d.so", i)), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// lazyShimRun runs script in a new process (see lazyShimConfig) with sys.path ending in
// path and the given environment, and returns its output; mode "noext" sets NoExtensions.
func lazyShimRun(t *testing.T, mode, path, script string, env ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-c", script)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PYTHAGO_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, lazyShimEnv+"="+mode, "PYTHAGO_TEST_LAZYSHIM_PATH="+path)
	cmd.Env = append(cmd.Env, env...)
	timer := time.AfterFunc(2*time.Minute, func() { cmd.Process.Kill() })
	defer timer.Stop()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

// Init does not load the shim on macOS, and pure-Python imports (built-in extension
// modules included) do not either; the first extension module import loads it once,
// with the importing thread's extension then binding the C API; later imports reuse it,
// and _imp.create_dynamic is CPython's own again.
func TestExtensionShimLazy(t *testing.T) {
	dir := probeExtensions(t, 2)
	out := lazyShimRun(t, "1", dir, `
import _shimprobe, _imp, sys
assert _shimprobe.shims() == [], ("loaded at Init", _shimprobe.shims())
assert type(_imp.create_dynamic).__name__ == "function", _imp.create_dynamic
import json, decimal, asyncio, threading, subprocess, sqlite3, ssl, hashlib, zipfile, socket
import select, unicodedata, pyexpat, xml.etree.ElementTree, multiprocessing, uuid, datetime
import concurrent.futures, array, struct, zlib, lzma, bz2, csv, email.parser, http.client
assert "ctypes" not in sys.modules
assert _shimprobe.shims() == [], ("loaded by a pure-Python import", _shimprobe.shims())
for name in ("lazyprobe0", "lazyprobe1"):
    try:
        __import__(name)
    except RuntimeError as e:
        assert str(e) == "probe %s initialized" % name, repr(e)
    else:
        raise AssertionError(name + " imported")
    shims = _shimprobe.shims()
    assert len(shims) == 1, shims
assert type(_imp.create_dynamic).__name__ == "builtin_function_or_method", _imp.create_dynamic
assert type(_imp.exec_builtin).__name__ == "builtin_function_or_method", _imp.exec_builtin
print("lazy ok")
`)
	if !strings.Contains(out, "lazy ok") {
		t.Fatal(out)
	}
}

// An extension imported during the main phase of initialization (sitecustomize, as .pth
// files and site-packages can) loads the shim: the hook is in place before site runs.
func TestExtensionShimLazySite(t *testing.T) {
	dir := probeExtensions(t, 1)
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "sitecustomize.py"), []byte(`
import sys
try:
    import lazyprobe0
except BaseException as e:
    sys.lazyprobe_site = e
`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := lazyShimRun(t, "1", site+string(os.PathListSeparator)+dir, `
import sys, _shimprobe
e = getattr(sys, "lazyprobe_site", None)
assert isinstance(e, RuntimeError) and str(e) == "probe lazyprobe0 initialized", repr(e)
assert len(_shimprobe.shims()) == 1, _shimprobe.shims()
print("site ok")
`)
	if !strings.Contains(out, "site ok") {
		t.Fatal(out)
	}
}

// Threads that import their first extensions at once all wait for the one load: each
// thread's extension binds the C API (a thread that went on before the shim was loaded
// would fail its dlopen with a missing flat-namespace symbol).
func TestExtensionShimLazyThreads(t *testing.T) {
	const n = 8
	dir := probeExtensions(t, n)
	out := lazyShimRun(t, "1", dir, fmt.Sprintf(`
import threading, _shimprobe
n = %d
barrier = threading.Barrier(n)
results = [None] * n
def imp(i):
    barrier.wait()
    try:
        __import__("lazyprobe%%d" %% i)
    except BaseException as e:
        results[i] = e
ts = [threading.Thread(target=imp, args=(i,)) for i in range(n)]
for t in ts: t.start()
for t in ts: t.join()
for i, e in enumerate(results):
    assert isinstance(e, RuntimeError) and str(e) == "probe lazyprobe%%d initialized" %% i, (i, repr(e))
assert len(_shimprobe.shims()) == 1, _shimprobe.shims()
print("threads ok")
`, n))
	if !strings.Contains(out, "threads ok") {
		t.Fatal(out)
	}
}

// ctypes loads the shim at a dlopen of a file (a library's initializers may bind the C
// API): here _ctypes.dlopen before ctypes is imported, so it is the _ctypes module's own
// function, and PyDLL of a file.
func TestExtensionShimLazyCtypes(t *testing.T) {
	dir := probeExtensions(t, 1)
	bundle := filepath.Join(dir, "lazyprobe0.so")
	out := lazyShimRun(t, "1", dir, fmt.Sprintf(`
import os, _shimprobe
import _ctypes
assert _shimprobe.shims() == [], _shimprobe.shims()
h = _ctypes.dlopen(%q, os.RTLD_NOW | os.RTLD_LOCAL)
assert h, h
assert len(_shimprobe.shims()) == 1, _shimprobe.shims()
assert _ctypes.dlsym(h, "PyInit_lazyprobe0")
print("_ctypes ok")
`, bundle))
	if !strings.Contains(out, "_ctypes ok") {
		t.Fatal(out)
	}
	out = lazyShimRun(t, "1", dir, fmt.Sprintf(`
import _shimprobe, ctypes
assert _shimprobe.shims() == [], _shimprobe.shims()
lib = ctypes.PyDLL(%q)
assert len(_shimprobe.shims()) == 1, _shimprobe.shims()
assert lib.PyInit_lazyprobe0
print("ctypes ok")
`, bundle))
	if !strings.Contains(out, "ctypes ok") {
		t.Fatal(out)
	}
}

// import ctypes does not load the shim, although it makes ctypes.pythonapi, PyDLL(None):
// dlopen(NULL) runs no code. Neither do CDLL(None) and PyDLL(None) themselves, nor ctypes
// work that looks no symbol up, nor their lookups of names outside the C API by
// attribute or item (the shim exports only ^_?Py), nor opening an image that is loaded
// already (3.14's ctypes.util opens libc at import) and looking symbols up through it,
// which finds no C API with the shim or without.
func TestExtensionShimLazyCtypesImport(t *testing.T) {
	out := lazyShimRun(t, "1", t.TempDir(), `
import _shimprobe, _imp, os, sys
def unloaded(what):
    assert _shimprobe.shims() == [], ("loaded by " + what, _shimprobe.shims())
import ctypes
unloaded("import ctypes")
assert ctypes.pythonapi is not None
import ctypes.util, ctypes.macholib.dyld
unloaded("import ctypes.util")
libs = [ctypes.CDLL(None), ctypes.PyDLL(None), ctypes.cdll.LoadLibrary(None), ctypes.pydll.LoadLibrary(None)]
cb = ctypes.CFUNCTYPE(ctypes.c_int, ctypes.c_int)(lambda x: x + 1)
assert cb(41) == 42
buf = ctypes.create_string_buffer(b"abc", 8)
ctypes.memmove(buf, b"xyz", 3)
assert buf.value == b"xyz" and ctypes.string_at(buf, 3) == b"xyz"
assert ctypes.sizeof(ctypes.c_void_p) == 8
unloaded("CDLL(None) or ctypes work without a lookup")
assert libs[0].getpid() == os.getpid() and libs[1]["getpid"]() == os.getpid()
assert ctypes.pythonapi.getpid() == os.getpid()
assert not hasattr(libs[2], "pythago_no_such_symbol") and getattr(libs[3], "pythago_none", 1) == 1
unloaded("a lookup of a name outside the C API through CDLL(None)")
libc = ctypes.CDLL(ctypes.util.find_library("c"))
system = ctypes.PyDLL("/usr/lib/libSystem.B.dylib")
import _ctypes
h = _ctypes.dlopen("/usr/lib/libSystem.B.dylib")
unloaded("a dlopen of a loaded image")
assert libc.getpid() == os.getpid()
assert ctypes.c_void_p.in_dll(system, "__stdoutp").value
assert not hasattr(system, "Py_IsInitialized") and not hasattr(libc, "PyLong_FromLong")
assert _ctypes.dlsym(h, "getpid")
if sys.version_info >= (3, 14):
    assert any(n.endswith("/libSystem.B.dylib") for n in ctypes.util.dllist())
unloaded("a lookup through a loaded image")
assert type(_imp.create_dynamic).__name__ == "function", _imp.create_dynamic
print("import ctypes ok")
`)
	if !strings.Contains(out, "import ctypes ok") {
		t.Fatal(out)
	}
}

// The first symbol lookup through a CDLL opened with name None loads the shim before
// dlsym runs, whichever way it is made: each case starts a process that has imported
// ctypes and not loaded the shim, makes one lookup of the C API, and checks its result.
// Afterwards CDLL's _handle and __getitem__ are the class's own again. A dlopen(None)
// that is not CDLL's, or CDLL's for a subclass that shadows _handle with a class
// attribute of its own (the property never sees its reads), loads the shim at once.
func TestExtensionShimLazyCtypesLookup(t *testing.T) {
	for _, c := range []struct {
		name, setup, check string
		eager              bool // the setup loads the shim
	}{
		{"attribute", "", "ctypes.pythonapi.Py_IsInitialized() == 1", false},
		{"item", "", `ctypes.pythonapi["Py_IsInitialized"]() == 1`, false},
		{"funcptr", "", `ctypes.pythonapi._FuncPtr(("Py_IsInitialized", ctypes.pythonapi))() == 1`, false},
		{"prototype", "", `ctypes.PYFUNCTYPE(ctypes.c_int)(("Py_IsInitialized", ctypes.pythonapi))() == 1`, false},
		{"in_dll", "", `ctypes.py_object.in_dll(ctypes.pythonapi, "PyExc_RuntimeError").value is RuntimeError`, false},
		{"dlsym", "import _ctypes", `_ctypes.dlsym(ctypes.pythonapi._handle, "Py_IsInitialized") != 0`, false},
		{"cdll", "lib = ctypes.CDLL(None)", "lib.Py_IsInitialized() == 1", false},
		{"subclass", "class Lib(ctypes.PyDLL): pass\nlib = Lib(None)", "lib.Py_IsInitialized() == 1", false},
		{"handle", `lib = ctypes.PyDLL("pythonapi", handle=ctypes.pythonapi.__dict__["_handle"])`, "lib.Py_IsInitialized() == 1", false},
		{"loader", "lib = ctypes.pydll.LoadLibrary(None)", "lib.Py_IsInitialized() == 1", false},
		{"raw dlopen", "import _ctypes", "_ctypes.dlopen(None) != 0", false},
		{"other first", "lib = ctypes.CDLL(None)\nassert lib.getpid() > 0 and lib['getpid']", "lib.Py_IsInitialized() == 1", false},
		{"hasattr", "", `hasattr(ctypes.pythonapi, "Py_IsInitialized")`, false},
		{"shadowed", "class Lib(ctypes.PyDLL): _handle = 0\nlib = Lib(None)", "lib.Py_IsInitialized() == 1", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := `assert _shimprobe.shims() == [], ("loaded before the lookup", _shimprobe.shims())`
			if c.eager {
				before = `assert len(_shimprobe.shims()) == 1, ("not loaded by the setup", _shimprobe.shims())`
			}
			out := lazyShimRun(t, "1", t.TempDir(), fmt.Sprintf(`
import _shimprobe, ctypes
%s
%s
assert %s
assert len(_shimprobe.shims()) == 1, ("not loaded by the lookup", _shimprobe.shims())
assert ctypes.CDLL.__dict__["_handle"] == 0, ctypes.CDLL.__dict__["_handle"]
assert "__wrapped__" not in vars(ctypes.CDLL.__getitem__), ctypes.CDLL.__getitem__
assert ctypes.pythonapi.Py_IsInitialized() == 1
print("lookup ok")
`, c.setup, before, c.check))
			if !strings.Contains(out, "lookup ok") {
				t.Fatal(out)
			}
		})
	}
}

// Threads whose first lookups of the C API through ctypes come at once all wait for the
// one load: each lookup finds its symbol.
func TestExtensionShimLazyCtypesThreads(t *testing.T) {
	out := lazyShimRun(t, "1", t.TempDir(), `
import threading, ctypes, _shimprobe
n = 8
barrier = threading.Barrier(n)
results = [None] * n
def lookup(i):
    lib = ctypes.PyDLL(None)
    barrier.wait()
    try:
        results[i] = lib.Py_IsInitialized()
    except BaseException as e:
        results[i] = e
ts = [threading.Thread(target=lookup, args=(i,)) for i in range(n)]
for t in ts: t.start()
for t in ts: t.join()
assert results == [1] * n, results
assert len(_shimprobe.shims()) == 1, _shimprobe.shims()
assert ctypes.CDLL.__dict__["_handle"] == 0, ctypes.CDLL.__dict__["_handle"]
print("ctypes threads ok")
`)
	if !strings.Contains(out, "ctypes threads ok") {
		t.Fatal(out)
	}
}

// A subinterpreter has its own _imp and _ctypes, which the hook does not reach, so the
// shim is loaded before the first one is created (not at import concurrent.interpreters):
// an extension imported first in a new interpreter binds the C API. The probe's PyInit
// runs (CPython runs a single-phase PyInit for an isolated interpreter in the main one,
// which prints its RuntimeError and raises ImportError in the subinterpreter); without
// the shim, the subinterpreter's dlopen fails with a missing flat-namespace symbol.
func TestExtensionShimLazySubinterpreter(t *testing.T) {
	dir := probeExtensions(t, 1)
	out := lazyShimRun(t, "1", dir, fmt.Sprintf(`
import sys, _shimprobe
sub = """
import sys
sys.path.append(%q)
try:
    import lazyprobe0
except ImportError as e:
    assert "symbol not found" not in str(e), str(e)
    print("subinterpreter import:", e)
else:
    raise AssertionError("imported")
"""
if sys.version_info >= (3, 14):
    from concurrent import interpreters
    assert _shimprobe.shims() == [], ("loaded by import concurrent.interpreters", _shimprobe.shims())
    interp = interpreters.create()
    assert len(_shimprobe.shims()) == 1, ("not loaded by interpreters.create", _shimprobe.shims())
    interp.exec(sub)
    interp.close()
else:
    import _interpreters
    assert _shimprobe.shims() == [], ("loaded by import _interpreters", _shimprobe.shims())
    iid = _interpreters.create()
    assert len(_shimprobe.shims()) == 1, ("not loaded by _interpreters.create", _shimprobe.shims())
    r = _interpreters.exec(iid, sub)
    assert r is None, r
    _interpreters.destroy(iid)
print("subinterpreter ok")
`, dir))
	if !strings.Contains(out, "subinterpreter ok") || !strings.Contains(out, "probe lazyprobe0 initialized") {
		t.Fatal(out)
	}
}

// A forked child cannot run Go, so the parent loads the shim before os.fork and the
// child inherits it: an extension the child imports binds the C API.
func TestExtensionShimLazyFork(t *testing.T) {
	dir := probeExtensions(t, 1)
	out := lazyShimRun(t, "1", dir, `
import os, sys, warnings, _shimprobe
warnings.simplefilter("ignore", DeprecationWarning)  # fork() of a multi-threaded process
assert _shimprobe.shims() == [], _shimprobe.shims()
r, w = os.pipe()
pid = os.fork()
if pid == 0:
    try:
        import lazyprobe0
    except BaseException as e:
        os.write(w, repr(e).encode())
    os._exit(0)
os.close(w)
with os.fdopen(r) as f:
    msg = f.read()
os.waitpid(pid, 0)
assert msg == "RuntimeError('probe lazyprobe0 initialized')", msg
assert len(_shimprobe.shims()) == 1, _shimprobe.shims()
print("fork ok")
`)
	if !strings.Contains(out, "fork ok") {
		t.Fatal(out)
	}
}

// Config.NoExtensions never loads the shim: extension modules and ctypes lookups of the
// C API fail, and nothing is wrapped.
func TestExtensionShimLazyNoExtensions(t *testing.T) {
	dir := probeExtensions(t, 1)
	out := lazyShimRun(t, "noext", dir, `
import _imp, _shimprobe
assert type(_imp.create_dynamic).__name__ == "builtin_function_or_method", _imp.create_dynamic
try:
    import lazyprobe0
except ImportError as e:
    assert "symbol not found in flat namespace" in str(e), repr(e)
else:
    raise AssertionError("imported")
import ctypes
assert ctypes.pythonapi is not None and not hasattr(ctypes.pythonapi, "Py_IsInitialized")
assert _shimprobe.shims() == [], _shimprobe.shims()
print("noext ok")
`)
	if !strings.Contains(out, "noext ok") {
		t.Fatal(out)
	}
}

// plainLibrary builds a dylib that binds nothing of the C API: int pythago_answer(void).
func plainLibrary(t *testing.T) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler to build a dylib with")
	}
	dir := t.TempDir()
	c := filepath.Join(dir, "plain.c")
	lib := filepath.Join(dir, "libplain.dylib")
	if err := os.WriteFile(c, []byte("int pythago_answer(void) { return 42; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(cc, "-dynamiclib", "-o", lib, c).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, out)
	}
	return lib
}

// A failed load (here an ExtensionShimDir that does not exist) is not retried (the
// directory created after the first failure is not used). Extension imports raise
// ImportError with the load's error, and the process goes on. ctypes then behaves as
// without the shim, with stock CPython's exceptions for a missing name: hasattr is False
// and getattr's default is returned, a lookup raises AttributeError, ValueError (in_dll)
// or OSError (dlsym), and a dlopen of a library that binds the C API fails in dyld; a
// library that does not bind it loads. The exceptions that pass through the hook's
// wrappers carry the load's error as their __cause__. Each case makes its first attempt
// at the load from a different place: an import, hasattr, getattr with a default.
func TestExtensionShimLazyError(t *testing.T) {
	dir := probeExtensions(t, 1)
	plain := plainLibrary(t)
	for _, c := range []struct{ name, first string }{
		{"import", `
try:
    import lazyprobe0
except ImportError as e:
    assert e.name == "lazyprobe0", e.name
    m = str(e)
else:
    raise AssertionError("imported")
os.makedirs(missing, exist_ok=True)  # too late: the load is not retried
`},
		{"hasattr", `
import ctypes
assert not hasattr(ctypes.pythonapi, "Py_IsInitialized")
os.makedirs(missing, exist_ok=True)
m = None
`},
		{"getattr", `
import ctypes
assert getattr(ctypes.pythonapi, "Py_IsInitialized", 1) == 1
os.makedirs(missing, exist_ok=True)
m = None
`},
	} {
		t.Run(c.name, func(t *testing.T) {
			missing := filepath.Join(t.TempDir(), "missing")
			out := lazyShimRun(t, "1", dir, fmt.Sprintf(`
import os, _shimprobe
missing, probe, plain = %q, %q, %q
%s
try:
    import lazyprobe0
except ImportError as e:
    assert e.name == "lazyprobe0", e.name
    m = m or str(e)
    assert str(e) == m, (str(e), m)
else:
    raise AssertionError("imported")
assert m.startswith("py: extension shim") and missing in m and "no such file or directory" in m, m

import ctypes, _ctypes
api = ctypes.pythonapi
def raises(exc, f, cause=True):
    try:
        f()
    except BaseException as e:
        assert type(e) is exc, (exc, repr(e))
        if cause:
            assert type(e.__cause__) is OSError and str(e.__cause__) == m, (repr(e), repr(e.__cause__))
        return str(e)
    raise AssertionError("no " + exc.__name__)
for lib in (api, ctypes.PyDLL(None), ctypes.CDLL(None)):
    assert not hasattr(lib, "Py_IsInitialized") and not hasattr(lib, "_Py_NoneStruct")
    assert getattr(lib, "PyLong_FromLong", None) is None
    assert "symbol not found" in raises(AttributeError, lambda: lib.Py_IsInitialized)
    assert "symbol not found" in raises(AttributeError, lambda: lib["PyLong_FromLong"])
raises(AttributeError, lambda: api._FuncPtr(("Py_IsInitialized", api)), cause=False)
raises(AttributeError, lambda: ctypes.PYFUNCTYPE(ctypes.c_int)(("Py_IsInitialized", api)), cause=False)
raises(ValueError, lambda: ctypes.c_int.in_dll(api, "Py_OptimizeFlag"), cause=False)
raises(OSError, lambda: _ctypes.dlsym(api._handle, "Py_IsInitialized"), cause=False)
assert "symbol not found in flat namespace" in raises(OSError, lambda: ctypes.CDLL(probe))
assert ctypes.CDLL(plain).pythago_answer() == 42
assert ctypes.CDLL(None).getpid() == os.getpid() and api.getpid() == os.getpid()
assert _shimprobe.shims() == [], _shimprobe.shims()
print("error ok")
`, missing, filepath.Join(dir, "lazyprobe0.so"), plain, c.first), "PYTHAGO_TEST_LAZYSHIM_DIR="+missing)
			if !strings.Contains(out, "error ok") {
				t.Fatal(out)
			}
		})
	}
}
