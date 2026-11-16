package py

import (
	"errors"
	"regexp"
	"sync"

	"github.com/moriyoshi/pythago/py/internal/extshim"
)

// exported matches the C API names the extension shim exports, as on Linux: only the
// interpreter's, never another library's (wheels' vendored copies must bind to their own).
var exported = regexp.MustCompile(`^_?Py`)

// loadExtensionShim makes the C API visible to C extensions. bao's loader, not dyld,
// mapped the image's libpython, so dyld knows none of its symbols; extensions, linked
// with -undefined dynamic_lookup, bind them through the flat namespace. The shim is a
// code-free dylib whose export trie holds their live addresses as absolute symbols,
// ad-hoc signed and loaded RTLD_GLOBAL from a file under cfg.ExtensionShimDir
// (os.TempDir if empty) that is removed once loaded.
//
// The addresses change with every run (ASLR), so the file is new each time, and macOS's
// first-load check of a new code file makes the load take about 0.3 s (M1 Max; 0.4 s on
// an i9). That is why Init does not call it on macOS: extensionShimHook loads it at the
// first load that can need it.
func loadExtensionShim(cfg Config) error {
	var syms []extshim.Symbol
	for name, addr := range symbols {
		if exported.MatchString(name) {
			syms = append(syms, extshim.Symbol{Name: name, Addr: addr})
		}
	}
	src := extshim.Source{Dir: cfg.ExtensionShimDir, Memfd: cfg.ExtensionShimMemfd}
	_, err := extshim.Load("libpython-pythago.dylib", syms, src)
	return err
}

func init() { lazyExtensionShim = extensionShimHook }

// extensionShimHook installs extensionShimHookSrc during initialization, after the core
// phase (importlib works) and before the main phase (site, .pth files and sitecustomize
// may import extensions). The hook calls a Go function that loads the shim once per
// process; concurrent callers wait for that load, outside Python (Release), so the
// loading thread needs neither the GIL nor a stop-the-world.
//
// A Python wrapper, rather than C, because everything that loads code needing the C API
// already goes through a few Python-visible entry points (see the source), and CPython
// keeps them as module attributes it looks up at each call; a C hook would have to
// interpose dlopen inside the bao-translated libpython.
func extensionShimHook(cfg Config) error {
	if cfg.ExtensionShimMemfd {
		// The one configuration error known before the load: fail Init, as before.
		return errors.New("extshim: macOS has no memfd: the shim loads from a file")
	}
	once := sync.OnceValue(func() error { return loadExtensionShim(cfg) })
	load, err := NewFunc("load_extension_shim", func([]*Object, Kwargs) (any, error) {
		var err error
		Release(func() { err = once() })
		if err != nil {
			return "py: extension shim (C extensions need it; Config.NoExtensions disables it): " + err.Error(), nil
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	defer load.Close()
	g, err := result(call0(cDictNew))
	if err != nil {
		return err
	}
	defer g.Close()
	builtins, err := Import("builtins")
	if err != nil {
		return err
	}
	defer builtins.Close()
	if err := g.SetItem("__builtins__", builtins); err != nil {
		return err
	}
	if err := g.SetItem("_load", load); err != nil {
		return err
	}
	return ExecIn(extensionShimHookSrc, g)
}

// extensionShimHookSrc wraps the entry points through which Python loads code that may
// bind the C API, or looks the C API up, so the first such load or lookup loads the shim
// first:
//   - _imp.create_dynamic, which ExtensionFileLoader (and so every extension module,
//     cffi's out-of-line modules among them) calls by attribute;
//   - _ctypes.dlopen, behind ctypes.CDLL, PyDLL and LibraryLoader: patched when the
//     _ctypes module (built in) is executed, before ctypes imports it as _dlopen. A
//     library's initializers may bind the C API, so a dlopen of a file loads the shim,
//     unless the image is loaded already (RTLD_NOLOAD finds it): then no initializer
//     runs, and a lookup through its handle searches that image and its dependencies,
//     never the shim (3.14's ctypes.util opens libc at import);
//   - ctypes.CDLL's _handle, for a CDLL opened with name None (ctypes.pythonapi, which
//     import ctypes makes, and CDLL(None)): dlopen(NULL) runs no code, so it does not load
//     the shim; the lookups do (ctypes needs the shim only to find a Py* name), and every
//     symbol lookup through a CDLL reads its _handle by attribute (__getattr__ and
//     __getitem__ through _FuncPtr((name, dll)), _FuncPtr and prototypes called with
//     (name, dll), in_dll, _ctypes.dlsym(dll._handle, name)). A property on CDLL stands in
//     for the class's _handle until the shim is loaded and loads it at the first read of
//     a CDLL's handle other than an already loaded image's (so CDLL(handle=) is covered
//     too), except while CDLL.__getitem__ (behind dll.name and dll["name"]) looks up a
//     name outside the C API, which the shim does not export. A dlopen(None) not made by
//     CDLL._load_library, or made for a subclass that shadows _handle, loads the shim at
//     once, as the returned handle reaches dlsym unseen;
//   - _interpreters.create, behind concurrent.interpreters (3.14) and
//     InterpreterPoolExecutor: a new interpreter has its own _imp and _ctypes, which these
//     wrappers do not reach, so the shim is loaded before the first one is created. An
//     error is not raised there: the new interpreter's extension imports then fail at
//     dlopen, and the main interpreter's report it;
//   - os.fork and everything else that runs PyOS_BeforeFork (multiprocessing's fork
//     and forkserver workers, pty.fork, subprocess with preexec_fn): a forked child
//     cannot run Go, so the parent loads the shim before forking and the child
//     inherits it.
//
// A failed load is not retried. Extension imports then raise ImportError with the load's
// error. ctypes behaves as without the shim, as CPython does for a missing name: a lookup
// of the C API raises AttributeError (so hasattr is False), ValueError (in_dll) or
// OSError (dlsym), and a dlopen of a library that binds it fails in dyld with OSError;
// where the exception passes through a wrapper (CDLL.__getitem__, _ctypes.dlopen), its
// __cause__ is an OSError with the load's error. Once the shim is loaded the wrappers put
// the originals back; a reference taken before (ctypes._dlopen) keeps a wrapper that only
// checks a flag.
const extensionShimHookSrc = `
def _install(load):
    import sys, _imp, _thread, posix

    loaded = False
    failed = None  # why the load failed; it is not retried
    wrapped = []  # (owner, name, original or missing, wrapper)
    missing = object()

    def ensure():
        # None once the shim is loaded, else why it is not.
        nonlocal loaded, failed
        if loaded:
            return None
        if failed is not None:
            return failed
        try:
            err = load()
        except Exception as e:  # a forked child, where Go functions cannot run
            err = "py: extension shim: %s" % e
        if err is None:
            loaded = True
            for w in wrapped:
                restore(*w)
        else:
            failed = err
        return err

    def blame(e):
        # Chain a failed load's error to e, which a lookup or load of the C API raised.
        if failed is not None and e.__cause__ is None:
            e.__cause__ = OSError(failed)

    def restore(owner, name, orig, wrapper):
        if owner.__dict__.get(name) is wrapper:
            if orig is missing:
                delattr(owner, name)
            else:
                setattr(owner, name, orig)

    def wrap(mod, name, wrapper, orig):
        wrapper.__name__ = orig.__name__
        wrapper.__qualname__ = getattr(orig, "__qualname__", orig.__name__)
        wrapper.__doc__ = orig.__doc__
        wrapper.__wrapped__ = orig
        wrapped.append((mod, name, orig, wrapper))
        setattr(mod, name, wrapper)

    create_dynamic = _imp.create_dynamic
    def create_dynamic_(spec, *file):
        err = ensure()
        if err is not None:
            raise ImportError(err, name=getattr(spec, "name", None), path=getattr(spec, "origin", None))
        return create_dynamic(spec, *file)
    wrap(_imp, "create_dynamic", create_dynamic_, create_dynamic)

    install_lock = _thread.allocate_lock()
    lazy_handles = {}  # CDLL class: the property that is its _handle until the load
    image_handles = set()  # handles of images that were loaded before their dlopen
    lookup = _thread._local()  # .other: CDLL.__getitem__ is looking up a name outside the C API

    def lazy_cdll(frame):
        # The CDLL class whose _load_library is running in frame, if any.
        cls = frame.f_globals.get("CDLL")
        if isinstance(cls, type) and getattr(cls.__dict__.get("_load_library"), "__code__", None) is frame.f_code:
            return cls
        return None

    def handle_on_lookup(cls):
        orig = cls.__dict__.get("_handle", missing)
        default = 0 if orig is missing else orig
        def get(self):
            handle = self.__dict__.get("_handle", default)
            if not loaded and handle not in image_handles and not getattr(lookup, "other", False):
                # A failed load leaves the C API out of the global scope, as without the
                # shim: the lookup fails as CPython's does for a missing name.
                ensure()
            return handle
        def set(self, handle):
            self.__dict__["_handle"] = handle
        prop = property(get, set, doc="The library's handle (pythago: reading it loads the extension shim).")
        entries = [(cls, "_handle", orig, prop)]
        getitem = cls.__dict__.get("__getitem__")
        if getitem is not None:
            def getitem_(self, name_or_ordinal):
                if loaded:
                    return getitem(self, name_or_ordinal)
                if isinstance(name_or_ordinal, str) and not name_or_ordinal.startswith(("Py", "_Py")):
                    # The shim exports only the C API (^_?Py): this lookup finds the
                    # same symbol, or none, with it or without.
                    other = getattr(lookup, "other", False)
                    lookup.other = True
                    try:
                        return getitem(self, name_or_ordinal)
                    finally:
                        lookup.other = other
                try:
                    return getitem(self, name_or_ordinal)
                except AttributeError as e:
                    blame(e)
                    raise
            getitem_.__name__ = getitem.__name__
            getitem_.__qualname__ = getitem.__qualname__
            getitem_.__doc__ = getitem.__doc__
            getitem_.__wrapped__ = getitem
            entries.append((cls, "__getitem__", getitem, getitem_))
        lazy_handles[cls] = prop
        for e in entries:
            wrapped.append(e)
            setattr(cls, e[1], e[3])
        if loaded:  # by another thread, which may have put the originals back already
            for e in entries:
                restore(*e)

    def handle_is_lazy(inst, cls):
        # Whether inst's class reads _handle through cls's property: a subclass can
        # shadow it with a class attribute of its own.
        for c in type(inst).__mro__:
            if "_handle" in c.__dict__:
                return c is cls and c.__dict__["_handle"] is lazy_handles.get(cls)
        return False

    def wrap_ctypes(mod):
        dlopen = mod.dlopen
        def dlopen_(*args):
            if loaded or not args:
                pass
            elif args[0] is None:  # dlopen(NULL): the handle searches every global image
                frame = sys._getframe(1)
                cls = lazy_cdll(frame)
                if cls is not None:
                    with install_lock:
                        if cls not in lazy_handles:
                            handle_on_lookup(cls)
                    if loaded or handle_is_lazy(frame.f_locals.get("self"), cls):
                        return dlopen(*args)
            else:
                mode = args[1] if len(args) > 1 else posix.RTLD_NOW | posix.RTLD_LOCAL
                try:
                    handle = dlopen(args[0], mode | posix.RTLD_NOLOAD)
                except OSError:  # not loaded yet
                    pass
                else:
                    image_handles.add(handle)
                    return handle
            if ensure() is None:
                return dlopen(*args)
            try:  # without the C API: a library that binds it fails in dyld
                return dlopen(*args)
            except OSError as e:
                blame(e)
                raise
        wrap(mod, "dlopen", dlopen_, dlopen)

    def wrap_interpreters(mod):
        create = mod.create
        def create_(*args, **kwargs):
            ensure()
            return create(*args, **kwargs)
        wrap(mod, "create", create_, create)

    hooks = {"_ctypes": wrap_ctypes, "_interpreters": wrap_interpreters,
             "_xxsubinterpreters": wrap_interpreters}
    pending = {}
    for name, hook in hooks.items():
        if name in sys.modules:
            hook(sys.modules[name])
        else:
            pending[name] = hook
    if pending:
        exec_builtin = _imp.exec_builtin
        def exec_builtin_(mod):
            r = exec_builtin(mod)
            hook = pending.get(getattr(mod, "__name__", None))
            if hook is not None and not loaded:
                hook(mod)
            return r
        wrap(_imp, "exec_builtin", exec_builtin_, exec_builtin)

    posix.register_at_fork(before=ensure)

_install(_load)
del _install, _load
`
