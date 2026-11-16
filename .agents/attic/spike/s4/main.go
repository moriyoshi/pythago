// Command s4 serves the stdlib and application code to Python from Go fs.FS values.
//
// Initialization runs in two phases: the isolated core phase only has builtin and
// frozen importers, so Go installs a _pythago_fs module and a meta-path finder
// before _Py_InitializeMain imports encodings, io, site and the rest.
//
//	s4 <stdlib-dir> [code]
package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/moriyoshi/pythago/internal/spike/pyapi"
	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

//go:embed all:app
var appFS embed.FS

func init() { runtime.LockOSThread() }

const root = "/pythago-fs"

// mounts maps virtual directories to the fs.FS serving them.
var mounts = map[string]fs.FS{}

func resolve(p string) (fs.FS, string, bool) {
	for prefix, fsys := range mounts {
		if p == prefix {
			return fsys, ".", true
		}
		if rel, ok := strings.CutPrefix(p, prefix+"/"); ok {
			return fsys, path.Clean(rel), true
		}
	}
	return nil, "", false
}

var (
	pythagoConfigNew        func() uintptr
	pythagoConfigFree       func(uintptr)
	pythagoConfigSetHome    func(cfg uintptr, home string, err *uintptr) int32
	pythagoConfigAppendPath func(cfg uintptr, p string, err *uintptr) int32
	pythagoInitializeCore   func(cfg uintptr, err *uintptr) int32
	pythagoInitializeMain   func(cfg uintptr, err *uintptr) int32

	pyFinalizeEx          func() int32
	pyRunSimpleString     func(string) int32
	pyCompileString       func(src, filename string, start int32) uintptr
	pyEvalEvalCode        func(code, globals, locals uintptr) uintptr
	pyErrPrint            func()
	pyErrSetString        func(exc uintptr, msg string)
	pyDictNew             func() uintptr
	pyDictSetItemString   func(d uintptr, key string, v uintptr) int32
	pyImportImportModule  func(string) uintptr
	pyImportGetModuleDict func() uintptr
	pyModuleNew           func(string) uintptr
	pyObjectSetAttrString func(obj uintptr, name string, v uintptr) int32
	pyLongFromLongLong    func(int64) uintptr
	pyLongAsLongLong      func(uintptr) int64
	pyUnicodeAsUTF8Size   func(obj uintptr, size *int) *byte
	pyUnicodeFromStrSize  func(s *byte, n int) uintptr
	pyBytesFromStrSize    func(s *byte, n int) uintptr
	pyListNew             func(int) uintptr
	pyListAppend          func(list, item uintptr) int32
	pyCMethodNew          func(def, self, module, cls uintptr) uintptr
	pyMemRawCalloc        func(n, size uintptr) uintptr
	pyIncRef              func(uintptr)
	pyDecRef              func(uintptr)
)

type goFunc func(args []uintptr) (uintptr, error)

var funcs []goFunc

func dispatch(self, args uintptr, nargs int, kwnames uintptr) uintptr {
	res, err := funcs[pyLongAsLongLong(self)](unsafe.Slice((*uintptr)(unsafe.Pointer(args)), nargs))
	if err != nil {
		pyErrSetString(pyapi.Pointer("PyExc_OSError"), err.Error())
		return 0
	}
	return res
}

func newBuiltin(name string, f goFunc, trampoline uintptr) uintptr {
	funcs = append(funcs, f)
	cname := pyMemRawCalloc(uintptr(len(name)+1), 1)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(cname)), len(name)), name)
	def := pyMemRawCalloc(1, 32) // PyMethodDef
	d := (*[4]uintptr)(unsafe.Pointer(def))
	d[0], d[1], d[2] = cname, trampoline, 0x80|0x02 // METH_FASTCALL|METH_KEYWORDS
	self := pyLongFromLongLong(int64(len(funcs) - 1))
	fn := pyCMethodNew(def, self, 0, 0)
	pyDecRef(self)
	return fn
}

func goString(obj uintptr) (string, error) {
	var n int
	p := pyUnicodeAsUTF8Size(obj, &n)
	if p == nil {
		return "", errors.New("expected str")
	}
	return string(unsafe.Slice(p, n)), nil
}

func pyStr(s string) uintptr { return pyUnicodeFromStrSize(unsafe.StringData(s), len(s)) }

func none() uintptr {
	n := pyapi.Addr("_Py_NoneStruct")
	pyIncRef(n)
	return n
}

// fsModule builds _pythago_fs: stat(path) -> 0 missing / 1 file / 2 dir,
// read(path) -> bytes | None, listdir(path) -> list[str].
func fsModule(trampoline uintptr) uintptr {
	mod := pyModuleNew("_pythago_fs")
	pathArg := func(args []uintptr) (fs.FS, string, bool, error) {
		if len(args) != 1 {
			return nil, "", false, errors.New("expected one path argument")
		}
		p, err := goString(args[0])
		if err != nil {
			return nil, "", false, err
		}
		fsys, rel, ok := resolve(p)
		if os.Getenv("S4_DEBUG") != "" {
			st, serr := fs.Stat(fsys, rel)
			fmt.Fprintf(os.Stderr, "debug: p=%q ok=%v rel=%q fsys=%T stat=(%v, %v)\n", p, ok, rel, fsys, st, serr)
		}
		return fsys, rel, ok, nil
	}
	defs := map[string]goFunc{
		"selftest": func(args []uintptr) (uintptr, error) {
			type box struct{ a, b *int }
			bad := 0
			var keep []*box
			for i := range 10000 {
				x, y := i, i+1
				b := &box{&x, &y}
				keep = append(keep, b)
			}
			for i, b := range keep {
				if b.a == nil || *b.a != i || *b.b != i+1 {
					bad++
				}
			}
			var pool sync.Pool
			pool.New = func() any { return new([]byte) }
			poolBad := 0
			for range 1000 {
				v, ok := pool.Get().(*[]byte)
				if !ok {
					poolBad++
					continue
				}
				pool.Put(v)
			}
			d1, e1 := appFS.ReadFile("app/hello/data.txt")
			d2, e2 := fs.ReadFile(mounts[root+"/app"], "hello/data.txt")
			f, e3 := appFS.Open("app/hello/data.txt")
			var st fs.FileInfo
			if f != nil {
				st, _ = f.Stat()
			}
			fmt.Fprintf(os.Stderr, "selftest: heapBad=%d poolBad=%d embed=%q,%v sub=%q,%v open=%v statNil=%v\n",
				bad, poolBad, d1, e1, d2, e2, e3, st == nil || reflect.ValueOf(st).IsNil())
			return none(), nil
		},
		"stat": func(args []uintptr) (uintptr, error) {
			fsys, rel, ok, err := pathArg(args)
			if err != nil {
				return 0, err
			}
			kind := int64(0)
			if ok {
				if st, err := fs.Stat(fsys, rel); err == nil {
					kind = 1
					if st.IsDir() {
						kind = 2
					}
				}
			}
			return pyLongFromLongLong(kind), nil
		},
		"read": func(args []uintptr) (uintptr, error) {
			fsys, rel, ok, err := pathArg(args)
			if err != nil {
				return 0, err
			}
			if !ok {
				return none(), nil
			}
			data, err := fs.ReadFile(fsys, rel)
			if err != nil {
				return none(), nil
			}
			if len(data) == 0 {
				data = []byte{0} // Valid pointer for an empty bytes object.
				return pyBytesFromStrSize(&data[0], 0), nil
			}
			return pyBytesFromStrSize(&data[0], len(data)), nil
		},
		"listdir": func(args []uintptr) (uintptr, error) {
			fsys, rel, ok, err := pathArg(args)
			if err != nil {
				return 0, err
			}
			list := pyListNew(0)
			if !ok {
				return list, nil
			}
			entries, _ := fs.ReadDir(fsys, rel)
			for _, e := range entries {
				s := pyStr(e.Name())
				pyListAppend(list, s)
				pyDecRef(s)
			}
			return list, nil
		},
	}
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fn := newBuiltin(n, defs[n], trampoline)
		pyObjectSetAttrString(mod, n, fn)
		pyDecRef(fn)
	}
	return mod
}

const finderSource = `
import sys
import _pythago_fs as _fs
from _frozen_importlib import ModuleSpec

ROOT = "/pythago-fs"


class GoFSLoader:
    def __init__(self, fullname, path, is_pkg):
        self.name, self.path, self._is_pkg = fullname, path, is_pkg

    def create_module(self, spec):
        return None

    def exec_module(self, module):
        exec(self.get_code(module.__name__), module.__dict__)

    def is_package(self, fullname):
        return self._is_pkg

    def get_filename(self, fullname=None):
        return self.path

    def get_data(self, path):
        data = _fs.read(path)
        if data is None:
            raise FileNotFoundError(2, "No such file or directory", path)
        return data

    def get_source(self, fullname):
        import importlib.util
        return importlib.util.decode_source(self.get_data(self.path))

    def get_code(self, fullname):
        return compile(self.get_data(self.path), self.path, "exec", dont_inherit=True)

    def get_resource_reader(self, fullname):
        if not self._is_pkg:
            return None
        from importlib.resources.abc import TraversableResources

        base = self.path.rpartition("/")[0]

        class Reader(TraversableResources):
            def files(self):
                return _gofs_path(base)

        return Reader()


_path_cls = None


def _gofs_path(path):
    # Built lazily: importlib.resources is not importable during the core phase.
    global _path_cls
    if _path_cls is None:
        import io
        from importlib.resources.abc import Traversable

        class GoFSPath(Traversable):
            def __init__(self, path):
                self._path = path

            @property
            def name(self):
                return self._path.rpartition("/")[2]

            def iterdir(self):
                for n in _fs.listdir(self._path):
                    yield GoFSPath(self._path + "/" + n)

            def is_dir(self):
                return _fs.stat(self._path) == 2

            def is_file(self):
                return _fs.stat(self._path) == 1

            def joinpath(self, *descendants):
                path = self._path
                for d in descendants:
                    for part in str(d).split("/"):
                        if part:
                            path += "/" + part
                return GoFSPath(path)

            def open(self, mode="r", *args, **kwargs):
                data = _fs.read(self._path)
                if data is None:
                    raise FileNotFoundError(2, "No such file or directory", self._path)
                if "b" in mode:
                    return io.BytesIO(data)
                return io.TextIOWrapper(io.BytesIO(data), *args, **kwargs)

            def __repr__(self):
                return "GoFSPath(%r)" % self._path

        _path_cls = GoFSPath
    return _path_cls(path)


class GoFSFinder:
    @classmethod
    def find_spec(cls, fullname, path=None, target=None):
        tail = fullname.rpartition(".")[2]
        for entry in sys.path if path is None else path:
            if not isinstance(entry, str) or not entry.startswith(ROOT):
                continue
            base = entry + "/" + tail
            init = base + "/__init__.py"
            if _fs.stat(base) == 2 and _fs.stat(init) == 1:
                spec = ModuleSpec(fullname, GoFSLoader(fullname, init, True), origin=init, is_package=True)
                spec.submodule_search_locations = [base]
                spec.has_location = True
                return spec
            if _fs.stat(base + ".py") == 1:
                spec = ModuleSpec(fullname, GoFSLoader(fullname, base + ".py", False), origin=base + ".py")
                spec.has_location = True
                return spec
        return None

    @classmethod
    def invalidate_caches(cls):
        pass


sys.meta_path.append(GoFSFinder)
`

func check(rc int32, cerr uintptr, what string) {
	if rc == 0 {
		return
	}
	msg := "unknown error"
	if cerr != 0 {
		var b []byte
		for p := cerr; *(*byte)(unsafe.Pointer(p)) != 0; p++ {
			b = append(b, *(*byte)(unsafe.Pointer(p)))
		}
		msg = string(b)
	}
	fmt.Fprintf(os.Stderr, "%s failed (%d): %s\n", what, rc, msg)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: s4 <stdlib-dir> [code]")
		os.Exit(2)
	}
	if os.Getenv("S4_DEBUG") != "" {
		st0, err0 := os.Stat(os.Args[1] + "/os.py")
		fmt.Fprintf(os.Stderr, "debug first: os.Stat nil=%v err=%v\n", st0 == nil, err0)
		st1, err1 := os.DirFS(os.Args[1]).(fs.StatFS).Stat("os.py")
		fmt.Fprintf(os.Stderr, "debug first: DirFS.Stat nil=%v err=%v\n", st1 == nil, err1)
	}
	start := time.Now()
	pyapi.Load(pyembed.SymbolAddr0())
	for _, f := range []struct {
		p    any
		name string
	}{
		{&pythagoConfigNew, "pythago_config_new"}, {&pythagoConfigFree, "pythago_config_free"},
		{&pythagoConfigSetHome, "pythago_config_set_home"},
		{&pythagoConfigAppendPath, "pythago_config_append_search_path"},
		{&pythagoInitializeCore, "pythago_initialize_core"},
		{&pythagoInitializeMain, "pythago_initialize_main"},
		{&pyFinalizeEx, "Py_FinalizeEx"}, {&pyRunSimpleString, "PyRun_SimpleString"},
		{&pyCompileString, "Py_CompileString"}, {&pyEvalEvalCode, "PyEval_EvalCode"},
		{&pyErrPrint, "PyErr_Print"}, {&pyErrSetString, "PyErr_SetString"},
		{&pyDictNew, "PyDict_New"}, {&pyDictSetItemString, "PyDict_SetItemString"},
		{&pyImportImportModule, "PyImport_ImportModule"},
		{&pyImportGetModuleDict, "PyImport_GetModuleDict"}, {&pyModuleNew, "PyModule_New"},
		{&pyObjectSetAttrString, "PyObject_SetAttrString"},
		{&pyLongFromLongLong, "PyLong_FromLongLong"}, {&pyLongAsLongLong, "PyLong_AsLongLong"},
		{&pyUnicodeAsUTF8Size, "PyUnicode_AsUTF8AndSize"},
		{&pyUnicodeFromStrSize, "PyUnicode_FromStringAndSize"},
		{&pyBytesFromStrSize, "PyBytes_FromStringAndSize"},
		{&pyListNew, "PyList_New"}, {&pyListAppend, "PyList_Append"},
		{&pyCMethodNew, "PyCMethod_New"}, {&pyMemRawCalloc, "PyMem_RawCalloc"},
		{&pyIncRef, "Py_IncRef"}, {&pyDecRef, "Py_DecRef"},
	} {
		pyapi.Register(f.p, f.name)
	}

	app, err := fs.Sub(appFS, "app")
	if err != nil {
		panic(err)
	}
	stdlibDir := root + "/lib/python3"
	mounts[stdlibDir] = os.DirFS(os.Args[1])
	mounts[root+"/app"] = app
	if os.Getenv("S4_DEBUG") != "" {
		w := (*[2]uintptr)(unsafe.Pointer(&app))
		dm, em := fs.ReadFile(app, "hello/data.txt")
		fmt.Fprintf(os.Stderr, "debug main: fs.ReadFile(sub)=%q,%v\n", dm, em)
		fmt.Fprintf(os.Stderr, "debug: subFS data=%#x words=%#x %#x\n", w[1], *(*uintptr)(unsafe.Pointer(w[1])), *(*uintptr)(unsafe.Pointer(w[1] + 8)))
	}

	if os.Getenv("S4_DEBUG") != "" {
		st, serr := fs.Stat(app, "hello/data.txt")
		fmt.Fprintf(os.Stderr, "debug main: embed stat=(%v, %v)\n", st.Name(), serr)
		st2, serr2 := fs.Stat(mounts[stdlibDir], "os.py")
		fmt.Fprintf(os.Stderr, "debug main: dirfs stat nil=%v err=%v\n", st2 == nil, serr2)
	}
	var cerr uintptr
	cfg := pythagoConfigNew()
	check(pythagoConfigSetHome(cfg, root, &cerr), cerr, "set home")
	check(pythagoConfigAppendPath(cfg, root+"/app", &cerr), cerr, "append path")
	check(pythagoConfigAppendPath(cfg, stdlibDir, &cerr), cerr, "append path")
	check(pythagoInitializeCore(cfg, &cerr), cerr, "core init")
	core := time.Since(start)

	// Core phase: install _pythago_fs and the finder.
	trampoline := purego.NewCallback(dispatch)
	mod := fsModule(trampoline)
	pyDictSetItemString(pyImportGetModuleDict(), "_pythago_fs", mod)
	pyDecRef(mod)
	globals := pyDictNew()
	builtins := pyImportImportModule("builtins")
	pyDictSetItemString(globals, "__builtins__", builtins)
	pyDecRef(builtins)
	code := pyCompileString(finderSource, "<pythago-finder>", 257) // Py_file_input
	if code == 0 || pyEvalEvalCode(code, globals, globals) == 0 {
		pyErrPrint()
		os.Exit(1)
	}

	if probe := os.Getenv("S4_PROBE"); probe != "" {
		pc := pyCompileString(probe, "<probe>", 257)
		if pc == 0 || pyEvalEvalCode(pc, globals, globals) == 0 {
			pyErrPrint()
			os.Exit(1)
		}
	}
	check(pythagoInitializeMain(cfg, &cerr), cerr, "main init")
	pythagoConfigFree(cfg)
	fmt.Printf("init: core %v, total %v\n", core.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))

	script := `
import sys, os, json, encodings, asyncio, traceback, importlib
print("sys.path", sys.path)
print("prefix", sys.prefix)
for m in ("encodings", "os", "json", "asyncio", "site"):
    mod = sys.modules.get(m)
    print(" ", m, getattr(mod, "__file__", None))
import hello
print("hello", hello.__file__, "->", hello.greeting())
try:
    hello.fail()
except ValueError:
    traceback.print_exc()
print("json", json.dumps({"from": "fs.FS"}))
print("asyncio", asyncio.run(asyncio.sleep(0, result="ok")))
`
	if len(os.Args) > 2 {
		script = os.Args[2]
	}
	rc := pyRunSimpleString(script)
	if pyFinalizeEx() != 0 || rc != 0 {
		os.Exit(1)
	}
}
