package py

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
	"unsafe"
)

// fsRoot is the virtual directory under which Go file systems appear to Python: module
// __file__ values, tracebacks and sys.path entries use these paths.
const fsRoot = "/pythago-fs"

// stdlibTree is a standard library tree: an fs.FS, or the bytes of a zip, whose stored
// (uncompressed) entries are then served in place.
type stdlibTree struct {
	fsys fs.FS
	zip  []byte
}

var registeredStdlib atomic.Pointer[stdlibTree]

// RegisterStdlib records the standard library tree (os.py, encodings/, json/, ... at its
// root; __pycache__/*.pyc files are used when their magic number matches), the default
// for [Config.Stdlib].
func RegisterStdlib(fsys fs.FS) { registeredStdlib.Store(&stdlibTree{fsys: fsys}) }

// RegisterStdlibZip is RegisterStdlib for a zip held in memory, typically embedded:
// entries stored without compression are served in place, without copies. Generated dist
// packages call it from init.
func RegisterStdlibZip(data []byte) { registeredStdlib.Store(&stdlibTree{zip: data}) }

// mount is one Go file system under fsRoot.
type mount struct {
	prefix string
	tree   stdlibTree
}

var mounts []mount

// stdlibSource decides where the standard library comes from: an explicit Config.Stdlib,
// else an installation on disk (Config.Home or PYTHONHOME), else the registered stdlib.
func stdlibSource(cfg Config, home string) *stdlibTree {
	if cfg.Stdlib != nil {
		return &stdlibTree{fsys: cfg.Stdlib}
	}
	if home != "" {
		return nil
	}
	return registeredStdlib.Load()
}

// fsSearchPaths mounts the stdlib and application file systems and returns the sys.path
// entries for them: the application first, then the stdlib.
func fsSearchPaths(cfg Config, stdlib *stdlibTree) []string {
	mounts = nil
	var paths []string
	if cfg.Files != nil {
		mounts = append(mounts, mount{fsRoot + "/app", stdlibTree{fsys: cfg.Files}})
		paths = append(paths, fsRoot+"/app")
	}
	if stdlib != nil {
		lib := fsRoot + "/lib/" + pythonLibDir()
		mounts = append(mounts, mount{lib, *stdlib})
		paths = append(paths, lib)
	}
	return paths
}

// pythonLibDir is "python3.13", or "python3.14t" for a free-threaded build, read from the
// image's Py_Version before initialization.
func pythonLibDir() string {
	v := *(*uint32)(unsafe.Pointer(symbol("Py_Version")))
	dir := fmt.Sprintf("python%d.%d", v>>24, v>>16&0xff)
	if freeThread {
		dir += "t"
	}
	return dir
}

// fsEntry matches pythago_fs_entry in pythago_py.c. data is 0 for a directory.
type fsEntry struct {
	path, pathLen, data, size uintptr
}

// The index and everything it points to stay reachable here for the life of the
// process; the Go heap does not move objects, so C may keep the addresses.
var (
	fsIndex []fsEntry
	fsKeep  [][]byte
	fsEmpty = []byte{0} // data of empty files: non-NULL, unlike directories
)

// maxFrozen is the most data a PyImport_FrozenModules entry can carry: its size is a C
// int.
const maxFrozen = math.MaxInt32

// fsFile locates a file's bytes: blob[off:off+size] of one of the blobs, or own (for
// empty files and those too large for a blob, blob is -1).
type fsFile struct {
	blob, off, size int
	own             []byte
}

// fsTree is a snapshot of every mount: the files, the directories, and the blobs holding
// the files' bytes. A blob is a mount's zip, whose stored entries are served in place,
// or an arena of copied files, starting with a byte that is not NUL. Each blob is also a
// frozen module's data (see installFrozen), so that every interpreter can read it.
type fsTree struct {
	files map[string]fsFile
	dirs  map[string]bool
	blobs [][]byte
}

// snapshot reads every mount into an fsTree.
func snapshot() (*fsTree, error) {
	t := &fsTree{files: map[string]fsFile{}, dirs: map[string]bool{}}
	type copied struct {
		path string
		data []byte
	}
	var copies []copied
	addDirs := func(p string) {
		for p != "/" && p != "." && p != "" {
			t.dirs[p] = true
			p = path.Dir(p)
		}
	}
	for _, m := range mounts {
		addDirs(m.prefix)
		if m.tree.zip != nil {
			z := m.tree.zip
			zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
			if err != nil {
				return nil, err
			}
			blob := -1
			if len(z) > 0 && len(z) <= maxFrozen && z[0] != 0 {
				blob = len(t.blobs)
				t.blobs = append(t.blobs, z)
			}
			for _, f := range zr.File {
				full := m.prefix + "/" + strings.TrimSuffix(f.Name, "/")
				if strings.HasSuffix(f.Name, "/") {
					addDirs(full)
					continue
				}
				addDirs(path.Dir(full))
				off, err := f.DataOffset()
				if f.Method == zip.Store && err == nil && off+int64(f.UncompressedSize64) <= int64(len(z)) {
					if blob >= 0 {
						t.files[full] = fsFile{blob: blob, off: int(off), size: int(f.UncompressedSize64)}
					} else {
						copies = append(copies, copied{full, z[off : off+int64(f.UncompressedSize64)]})
					}
					continue
				}
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				data, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					return nil, err
				}
				copies = append(copies, copied{full, data})
			}
			continue
		}
		err := fs.WalkDir(m.tree.fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			full := m.prefix
			if p != "." {
				full += "/" + p
			}
			if d.IsDir() {
				addDirs(full)
				return nil
			}
			data, err := fs.ReadFile(m.tree.fsys, p)
			if err != nil {
				return err
			}
			copies = append(copies, copied{full, data})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// Lay the copies out in arenas of at most maxFrozen bytes, each allocated once.
	base := len(t.blobs)
	var sizes []int
	for _, c := range copies {
		switch n := len(c.data); {
		case n == 0:
			t.files[c.path] = fsFile{blob: -1}
		case n >= maxFrozen:
			t.files[c.path] = fsFile{blob: -1, size: n, own: c.data}
		default:
			if len(sizes) == 0 || sizes[len(sizes)-1]+n > maxFrozen {
				sizes = append(sizes, 1) // a frozen module's data must not start with NUL
			}
			t.files[c.path] = fsFile{blob: base + len(sizes) - 1, off: sizes[len(sizes)-1], size: n}
			sizes[len(sizes)-1] += n
		}
	}
	for _, n := range sizes {
		arena := make([]byte, n)
		arena[0] = 1
		t.blobs = append(t.blobs, arena)
	}
	for _, c := range copies {
		if f := t.files[c.path]; f.blob >= base {
			copy(t.blobs[f.blob][f.off:f.off+f.size], c.data)
		}
	}
	return t, nil
}

// paths returns every file and directory path, sorted in byte order (as pythago_py.c
// compares).
func (t *fsTree) paths() []string {
	paths := make([]string, 0, len(t.files)+len(t.dirs))
	for p := range t.files {
		paths = append(paths, p)
	}
	for p := range t.dirs {
		if _, ok := t.files[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

// data returns a file's bytes.
func (t *fsTree) data(f fsFile) []byte {
	if f.blob < 0 {
		return f.own
	}
	return t.blobs[f.blob][f.off : f.off+f.size]
}

// buildIndex builds the C index of the tree.
func buildIndex(t *fsTree, paths []string) {
	fsIndex = make([]fsEntry, len(paths))
	fsKeep = nil
	for i, p := range paths {
		pb := []byte(p)
		fsKeep = append(fsKeep, pb)
		e := fsEntry{path: uintptr(unsafe.Pointer(&pb[0])), pathLen: uintptr(len(pb))}
		if f, ok := t.files[p]; ok {
			data := t.data(f)
			e.data, e.size = uintptr(unsafe.Pointer(&fsEmpty[0])), uintptr(len(data))
			if len(data) > 0 {
				e.data = uintptr(unsafe.Pointer(&data[0]))
			}
		}
		fsIndex[i] = e
	}
	for _, f := range t.files {
		if f.own != nil {
			fsKeep = append(fsKeep, f.own)
		}
	}
}

// pyIndex is the tree's index for fsModuleSrc, in marshal format: (number of blobs,
// {path: (blob, offset, size) for a file, or [names] for a directory}). Files that no
// blob holds (too large for one) and paths that are not UTF-8 are left out.
func pyIndex(t *fsTree, paths []string) []byte {
	// A directory's names are those of the paths that are its own plus one more
	// component, as the C module scans for them: "" lists fsRoot.
	children := map[string][]string{}
	for _, p := range paths {
		if i := strings.LastIndexByte(p, '/'); i >= 0 && utf8.ValidString(p) && (t.dirs[p[:i]] || i == 0) {
			children[p[:i]] = append(children[p[:i]], p[i+1:])
		}
	}
	n := 0
	for _, p := range paths {
		n += 2*len(p) + 32 // the path, and its name in its directory's list or a tuple
	}
	w := marshalWriter{b: make([]byte, 0, n)}
	w.b = append(w.b, ')', 2)
	w.int(len(t.blobs))
	w.b = append(w.b, '{')
	if names := children[""]; len(names) > 0 {
		paths = append([]string{""}, paths...) // listed, but not a directory (see stat)
	}
	for _, p := range paths {
		if !utf8.ValidString(p) {
			continue
		}
		if f, ok := t.files[p]; ok {
			if f.blob < 0 && f.size > 0 {
				continue
			}
			w.str(p)
			w.b = append(w.b, ')', 3)
			w.int(max(f.blob, 0))
			w.int(f.off)
			w.int(f.size)
			continue
		}
		w.str(p)
		names := children[p]
		w.b = append(w.b, '[')
		w.long(len(names))
		for _, n := range names {
			w.str(n)
		}
	}
	w.b = append(w.b, '0') // the end of the dict
	return w.b
}

// marshalWriter writes the few marshal types pyIndex needs, whose encoding has not
// changed across marshal versions.
type marshalWriter struct{ b []byte }

func (w *marshalWriter) long(n int) { w.b = binary.LittleEndian.AppendUint32(w.b, uint32(int32(n))) }

func (w *marshalWriter) int(n int) {
	w.b = append(w.b, 'i')
	w.long(n)
}

func (w *marshalWriter) str(s string) {
	w.b = append(w.b, 'u')
	w.long(len(s))
	w.b = append(w.b, s...)
}

// frozenEntry matches CPython's struct _frozen (cpython/import.h, 3.11 and later).
type frozenEntry struct {
	name, code      *byte
	size, isPackage int32
}

// The frozen modules table and everything it points to, kept for the life of the
// process.
var (
	frozenTable []frozenEntry
	frozenKeep  [][]byte
)

// Frozen module names. Those of data entries cannot be imported by an import statement.
const (
	frozenIndexName = "<pythago-fs index>"
	frozenBlobName  = "<pythago-fs data %d>"
)

// installFSImporter runs in the core phase of initialization, before anything is
// imported from a file system: it adds _pythago_fs (C, over the index), and frozen
// modules through which every interpreter, subinterpreters included, gets the importer
// (see installFrozen); then it imports _pythago_fsimport, which adds a sys.path hook for
// fsRoot entries. app is the application's mount, "" if none.
func installFSImporter(app string) error {
	t, err := snapshot()
	if err != nil {
		return err
	}
	paths := t.paths()
	buildIndex(t, paths)
	var first uintptr
	if len(fsIndex) > 0 {
		first = uintptr(unsafe.Pointer(&fsIndex[0]))
	}
	if int32(call2(cPyFsInstall, first, uintptr(len(fsIndex)))) != 0 {
		return fetchError()
	}
	if err := installFrozen(t, pyIndex(t, paths), app); err != nil {
		return err
	}
	m, err := Import("_pythago_fsimport")
	if err != nil {
		return err
	}
	m.Close()
	return nil
}

// installFrozen sets PyImport_FrozenModules, which every interpreter of the process
// consults when importing, ahead of the frozen standard library modules, to a table of:
//
//   - _pythago_fsimport (fsImporterSrc), which adds the path hook to the interpreter that
//     imports it;
//   - _pythago_fs (fsModuleSrc), the Python counterpart of the C module, which only the
//     main interpreter has (as a module made outside the import system, CPython would not
//     create it again in another interpreter);
//   - encodings (encodingsSrc), which stands in for the package. Each interpreter imports
//     encodings first of all from a file system, while it is created (the main one in the
//     main phase of initialization): this one imports _pythago_fsimport, then loads the
//     real package in its place.
//   - the data that _pythago_fs reads, which _imp.find_frozen(name, withdata=True)
//     returns as a memoryview, without copying: the index, and one entry per blob.
//
// It is all C and Python, so it works in forked children, where Go cannot run.
func installFrozen(t *fsTree, index []byte, app string) error {
	appRepr := "None"
	if app != "" {
		appRepr = strconv.Quote(app) // a Python literal too, for a path of ASCII
	}
	srcs := map[string]string{
		"_pythago_fsimport": fsImporterSrc,
		"_pythago_fs":       fsModuleSrc,
		"encodings":         strings.Replace(encodingsSrc, "APP = None", "APP = "+appRepr, 1),
	}
	g, err := result(call0(cDictNew))
	if err != nil {
		return err
	}
	defer g.Close()
	if err := g.SetItem("srcs", srcs); err != nil {
		return err
	}
	if err := ExecIn(`
import marshal
codes = {n: marshal.dumps(compile(s, "<frozen %s>" % n, "exec", dont_inherit=True)) for n, s in srcs.items()}
`, g); err != nil {
		return err
	}
	codes, err := g.Item("codes")
	if err != nil {
		return err
	}
	defer codes.Close()
	frozenTable, frozenKeep = nil, nil
	add := func(name string, data []byte, pkg bool) {
		n := append([]byte(name), 0)
		frozenKeep = append(frozenKeep, n, data)
		e := frozenEntry{name: &n[0], code: &data[0], size: int32(len(data))}
		if pkg {
			e.isPackage = 1
		}
		frozenTable = append(frozenTable, e)
	}
	for _, name := range []string{"_pythago_fsimport", "_pythago_fs", "encodings"} {
		c, err := codes.Item(name)
		if err != nil {
			return err
		}
		data, err := c.Bytes()
		c.Close()
		if err != nil {
			return err
		}
		add(name, data, name == "encodings")
	}
	if len(index) > maxFrozen {
		return errors.New("py: the file systems' index is too large")
	}
	add(frozenIndexName, index, false)
	for i, b := range t.blobs {
		add(fmt.Sprintf(frozenBlobName, i), b, false)
	}
	addr := symbol("PyImport_FrozenModules")
	if addr == 0 {
		return errors.New("py: PyImport_FrozenModules is missing from the native image")
	}
	cell := (*uintptr)(unsafe.Pointer(addr))
	if prev := *cell; prev != 0 { // keep an embedder's own table, after ours
		for e := (*frozenEntry)(unsafe.Pointer(prev)); e.name != nil; e = (*frozenEntry)(unsafe.Add(unsafe.Pointer(e), unsafe.Sizeof(*e))) {
			frozenTable = append(frozenTable, *e)
		}
	}
	frozenTable = append(frozenTable, frozenEntry{})
	*cell = uintptr(unsafe.Pointer(&frozenTable[0]))
	return nil
}

// fsModuleSrc is _pythago_fs for interpreters other than the main one, over the same
// index as the C module, read from frozen data.
const fsModuleSrc = `
"""Go file systems mounted under /pythago-fs."""

import _imp, marshal


def _data(name):
    return _imp.find_frozen(name, withdata=True)[0]


_count, _index = marshal.loads(_data("` + frozenIndexName + `"))
_blobs = [_data("` + frozenBlobName + `" % i) for i in range(_count)]
del _data


def _check(path):
    if not isinstance(path, str):
        raise TypeError("bad argument type for built-in operation")


def stat(path):
    """0: missing, 1: file, 2: directory."""
    _check(path)
    v = _index.get(path)
    return 0 if v is None or not path else 1 if type(v) is tuple else 2


def read(path):
    """The file's bytes, or None."""
    _check(path)
    v = _index.get(path)
    if type(v) is not tuple:
        return None
    blob, off, size = v
    return _blobs[blob][off : off + size].tobytes() if size else b""


def listdir(path):
    """Names in a directory (empty if missing)."""
    _check(path)
    v = _index.get(path)
    return list(v) if type(v) is list else []
`

// encodingsSrc is the frozen encodings (see installFrozen). APP is the application's
// mount, which goes first on sys.path where the configuration does not have it (the
// standard library is on disk).
const encodingsSrc = `
APP = None


def _load():
    import sys
    import _pythago_fsimport
    from _frozen_importlib import _load_unlocked
    from _frozen_importlib_external import PathFinder

    if APP is not None and APP not in sys.path:
        sys.path.insert(0, APP)
    spec = PathFinder.find_spec(__name__)
    if spec is None:
        raise ModuleNotFoundError("No module named %r" % __name__, name=__name__)
    _load_unlocked(spec)  # replaces this module in sys.modules


_load()
`

// fsImporterSrc is a path-entry finder for fsRoot entries, so CPython's own PathFinder
// handles packages, namespace packages and pkgutil.iter_modules. It must work in the
// core phase, before importlib's Python-level modules are importable.
const fsImporterSrc = `
import sys, marshal
import _pythago_fs as _fs
import _frozen_importlib as _bs
import _frozen_importlib_external as _bx

ROOT = "/pythago-fs"


def _norm(path):
    # On Windows, Python code joins with backslashes (os.path.join in pkgutil.get_data),
    # and site.py makes sys.path entries absolute, which turns /pythago-fs/x into
    # Z:\pythago-fs\x on the current drive.
    if sys.platform == "win32" and isinstance(path, str):
        path = path.replace("\\", "/")
        if path[1:3] == ":/":
            path = path[2:]
    return path


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
        data = _fs.read(_norm(path))
        if data is None:
            raise FileNotFoundError(2, "No such file or directory", path)
        return data

    def get_source(self, fullname):
        return _bx.decode_source(self.get_data(self.path))

    def get_code(self, fullname):
        head, _, tail = self.path.rpartition("/")
        tag = sys.implementation.cache_tag
        if tag is not None:
            pyc = _fs.read("%s/__pycache__/%s.%s.pyc" % (head, tail[:-3], tag))
            if pyc is not None and pyc[:4] == _bx.MAGIC_NUMBER:
                return marshal.loads(memoryview(pyc)[16:])
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
    """Path-entry finder for one directory of a Go file system."""

    def __init__(self, path):
        self.path = path

    def find_spec(self, fullname, target=None):
        tail = fullname.rpartition(".")[2]
        base = self.path + "/" + tail
        if _fs.stat(base) == 2:
            init = base + "/__init__.py"
            if _fs.stat(init) == 1:
                spec = _bs.ModuleSpec(fullname, GoFSLoader(fullname, init, True), origin=init, is_package=True)
                spec.submodule_search_locations = [base]
                spec.has_location = True
                return spec
            spec = _bs.ModuleSpec(fullname, None, is_package=True)  # a namespace portion
            spec.submodule_search_locations = [base]
            return spec
        if _fs.stat(base + ".py") == 1:
            spec = _bs.ModuleSpec(fullname, GoFSLoader(fullname, base + ".py", False), origin=base + ".py")
            spec.has_location = True
            return spec
        return None

    def invalidate_caches(self):
        pass

    def iter_modules(self, prefix=""):
        for name in sorted(_fs.listdir(self.path)):
            full = self.path + "/" + name
            if name.endswith(".py") and name != "__init__.py" and _fs.stat(full) == 1:
                yield prefix + name[:-3], False
            elif "." not in name and _fs.stat(full + "/__init__.py") == 1:
                yield prefix + name, True


def _path_hook(path):
    path = _norm(path)
    if isinstance(path, str) and (path == ROOT or path.startswith(ROOT + "/")) and _fs.stat(path) == 2:
        return GoFSFinder(path)
    raise ImportError("not a Go file system path")


sys.path_hooks.insert(0, _path_hook)
sys.path_importer_cache.clear()
`
