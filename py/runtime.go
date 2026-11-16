// Package py embeds CPython in a Go program built with CGO_ENABLED=0.
//
// # Threads
//
// Every Python thread is backed by a goroutine. When Python starts a thread
// (threading.Thread, _thread.start_new_thread), package py starts a goroutine locked to
// its own OS thread and runs the thread there; the goroutine exits, and its OS thread
// with it, once the thread has been joined or detached. Python thread identity
// (get_ident, native_id, threading.local) is therefore that of an ordinary OS thread, and
// Go callbacks made from a Python thread run on that thread's own stack.
//
// Goroutines enter Python with [Do]. Do runs its function on the calling goroutine,
// locked to the current OS thread, with a Python thread state attached. Each OS thread
// keeps one cached thread state, so Python thread identity under Do is per OS thread,
// not per goroutine. An admission semaphore ([Config.Slots]) bounds how many goroutines
// are inside Python at once, and so how many OS threads Go→Python calls occupy. Inside
// Do, or inside a Go function called from Python, [Release] detaches the thread state
// around blocking Go code so other Python threads (and free-threaded stop-the-world
// pauses) are not held up.
//
// Python thread states are never shared: an OS thread has at most one, as CPython
// assumes.
//
// # Objects
//
// An [*Object] owns one reference. Close releases it; an unclosed Object is released after
// Go's garbage collector finds it unreachable, the next time a goroutine enters Python.
// Object methods, conversions and the other functions of this package must be called
// inside Do or inside a Go function called from Python. The arguments of a [Func] are
// borrowed instead: valid until it returns, after which using one panics until a later
// call reuses its memory (always, with [Config.CheckBorrows]); keep one with NewRef.
package py

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Config configures the embedded interpreter.
type Config struct {
	// Symtab is the address of the native image's pythago_symtab. It defaults to the
	// image registered with [RegisterImage], which generated dist packages do when
	// imported.
	Symtab uintptr
	// Stdlib serves the standard library (os.py, encodings/, ... at its root) from a Go
	// file system. It defaults to the stdlib registered with [RegisterStdlib], which dist
	// packages embed, unless Home or PYTHONHOME names an installation on disk. C
	// extension builds that need Python's headers (torch.compile, JIT kernels) need the
	// installation on disk.
	Stdlib fs.FS
	// Files serves application code, first on sys.path. Modules see virtual paths under
	// /pythago-fs as __file__; read package data with importlib.resources or
	// pkgutil.get_data rather than open().
	Files fs.FS
	// Home is a Python installation prefix on disk holding the standard library. If empty,
	// the PYTHONHOME environment variable is used.
	Home string
	// ProgramName sets sys.executable's base name. Defaults to os.Args[0].
	ProgramName string
	// SearchPaths, if set, replaces the computed sys.path.
	SearchPaths []string
	// Slots bounds the goroutines inside Python through Do at once. The default is
	// GOMAXPROCS on free-threaded builds and min(4, GOMAXPROCS) with the GIL, where more
	// slots only add contention for the GIL. A Do that gives its slot back in [Release]
	// gets one again ahead of goroutines entering Do.
	Slots int
	// MaxThreads caps live Python threads; starting one more raises RuntimeError("can't
	// start new thread") in Python. 0 means 4096. Go's own thread limit
	// (debug.SetMaxThreads) is raised to fit if needed. Not on Windows, where CPython
	// starts its own threads.
	MaxThreads int
	// SpareThreads is how many idle goroutine-backed OS threads to keep ready for new
	// Python threads, which then start without waiting for Go to create an OS thread
	// (about 50 us). Each Python thread still gets a fresh OS thread. 0 means 4; negative
	// disables spares. None on Windows (no goroutine-backed threads).
	SpareThreads int
	// Environment is a venv-like directory (bin/, lib/pythonX.Y/site-packages): its
	// site-packages is added with site.addsitedir (so .pth files work), its bin/ is put
	// first on PATH, and sys.prefix and VIRTUAL_ENV point at it. An existing venv works if
	// its Python version and ABI match the native image.
	Environment string
	// Modules are Go-defined modules, created at initialization in this process and in
	// processes started from sys.executable (multiprocessing workers), so that both can
	// import them. Values are converted as for [NewModule].
	Modules map[string]map[string]any
	// PythonSignals lets Python install its own signal handlers (SIGINT raises
	// KeyboardInterrupt), as the python command does; SIGINT then no longer reaches Go
	// (os/signal). By default Go keeps all signals; on Windows, where Go handles Ctrl+C as
	// a console event, py sets SIGINT back to SIG_DFL in the C runtime so that it passes
	// the event on.
	PythonSignals bool
	// MultiprocessingStart is multiprocessing's default start method. The default,
	// "forkserver" (Python 3.14's Linux default), gives workers a fresh process in which
	// Go functions work; in a forked child ("fork") they cannot run. "default" keeps
	// Python's own default. Code may still choose a method explicitly. On Windows, where
	// spawn is the only method, the default is Python's.
	MultiprocessingStart string
	// NoExtensions never loads the extension shim, which makes the C API visible to C
	// extensions: extension modules then fail to import. On Linux, Init loads the shim (it
	// needs glibc 2.28 or later). On macOS, where it is a new, signed dylib in every
	// process and macOS's first-load check of it costs about 0.3-0.5 s, it is loaded once,
	// at the first load that can need it: an extension module import, ctypes.CDLL or
	// PyDLL (import ctypes is one: ctypes.pythonapi), or os.fork (forked children cannot
	// load it); a failed load raises ImportError or OSError there.
	NoExtensions bool
	// ExtensionShimDir is the directory (Linux, macOS) under which the extension shim is
	// written to a new temporary directory, loaded and removed. It defaults to os.TempDir
	// ($TMPDIR), falling back to a memfd on Linux if that fails; when set, a failure is an
	// error (at Init on Linux, at the first extension load on macOS).
	ExtensionShimDir string
	// ExtensionShimMemfd loads the extension shim from a memfd (Linux only; an error on
	// macOS), for systems with no writable directory. The shim is then named
	// /proc/self/fd/N in the link map, and gdb, which opens every loaded object by its name
	// in its own process, hangs attaching to the program (N is often one of gdb's pipes).
	// It excludes ExtensionShimDir.
	ExtensionShimMemfd bool
	// ImageCacheDir is the directory (Windows) to which Init writes the native image's
	// files, one image-<digest> directory per image, and from which it removes the other
	// images that no running process uses (each process marks its image in use until it
	// exits). It defaults to pythago under os.UserCacheDir (%LOCALAPPDATA%\pythago), or
	// under os.TempDir if there is none. [ImageInfo], which takes no Config, writes the
	// image to the default directory, and Init then uses that copy.
	ImageCacheDir string
	// CheckBorrows never reuses the memory of a Go function's borrowed arguments (Func):
	// a *Object kept past its call, which a Func must not do (keep a NewRef), then always
	// panics when used. Without it, such an Object panics only until a later call reuses
	// its memory, and then reads that call's argument instead. It costs every Python→Go
	// call an allocation; for tests and debugging.
	CheckBorrows bool
	// FusedBytes, when positive, turns on Go-owned bytes objects (the fusion), with at most
	// this many bytes of Go memory for them, in 1 MiB chunks made as they are needed and
	// given back to Go when they have been empty for a while: a request body that package
	// asgi reads (up to 64 KiB per chunk) becomes a Python bytes object in that memory,
	// without a copy. 0, the default, installs nothing.
	//
	// It wraps Python's object allocator (PyObject_Free), which costs about 1% on every
	// object free, and Init fails if the image's CPython is not one the fusion is verified
	// on (3.13 with the GIL, 3.14 free-threaded) or its startup check fails. On
	// free-threaded builds memory changes size class only after a garbage collection, so
	// with gc disabled a full cap falls back to copies. Tools that replace the object
	// allocator without wrapping the one installed (rather than PyMem_GetAllocator and
	// chaining it, as tracemalloc does) break it. See the README for sizing (8-16 MiB).
	FusedBytes int
	// Setup, if set, runs on the main thread right after initialization, with its
	// thread state attached, before Init returns or Main runs the command line. Like
	// Modules and Environment, it applies to processes started from sys.executable too.
	Setup func() error
}

var (
	// ErrNotRunning is returned when the interpreter is not initialized, or finalized.
	ErrNotRunning = errors.New("py: interpreter is not running")
	// ErrNoThreadState is returned by Do when no Python thread state could be created.
	ErrNoThreadState = errors.New("py: cannot create a Python thread state")
)

const (
	stateNew = iota
	stateRunning
	stateFinalizing
	stateFinalized
)

var (
	state      atomic.Int32
	initMu     sync.Mutex
	slots      chan struct{}
	finalizeCh chan chan error
	freeThread bool
	// imageCacheDir is Config.ImageCacheDir, for the Windows image loader.
	imageCacheDir string
)

// Init initializes the interpreter. It may be called once per process.
//
// sys.executable is this program. Processes that Python starts from it (multiprocessing's
// spawn and forkserver start methods, subprocess.run([sys.executable, ...])) carry a
// marker in their environment; a program detects them with [IsPythonCommand] and runs
// them with [Main].
func Init(cfg Config) error {
	initMu.Lock()
	defer initMu.Unlock()
	errc, _, err := start(cfg, nil)
	if err != nil {
		return err
	}
	return <-errc
}

// executableEnv names the environment variable that marks processes started from
// sys.executable; its value is the executable's path, so other programs that embed
// package py and inherit the variable are not mistaken for one.
const executableEnv = "PYTHAGO_PYTHON_EXECUTABLE"

// IsPythonCommand reports whether Python started this process as sys.executable, so it
// should behave like the python command: call [Main] with os.Args.
func IsPythonCommand() bool {
	exe, err := os.Executable()
	return err == nil && os.Getenv(executableEnv) == exe
}

// Main runs the python command line argv (argv[0] is the program) as CPython's own
// `python` would (-c, -m, -I, a script path, ...), with cfg's configuration, and returns
// its exit status. The interpreter is finalized when it returns.
func Main(cfg Config, argv []string) int {
	initMu.Lock()
	errc, rcc, err := start(cfg, argv)
	if err == nil {
		err = <-errc
	}
	initMu.Unlock()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return <-rcc
}

// lazyExtensionShim, if set (macOS), installs a hook that loads the extension shim at the
// first load that can need it, instead of Init loading it: it runs after the core phase
// of initialization.
var lazyExtensionShim func(Config) error

// start prepares package py and starts the main Python thread; with argv it runs the
// command line there and sends its exit status on the second channel.
func start(cfg Config, argv []string) (<-chan error, <-chan int, error) {
	if state.Load() != stateNew {
		return nil, nil, errors.New("py: the interpreter was already initialized")
	}
	checkBorrows = cfg.CheckBorrows
	symtab := cfg.Symtab
	if symtab == 0 {
		var err error
		imageCacheDir = cfg.ImageCacheDir
		if symtab, err = loadImage(); err != nil {
			return nil, nil, err
		}
	}
	if symtab == 0 {
		return nil, nil, errors.New("py: no native image: import a pythago dist package or set Config.Symtab")
	}
	if err := resolve(symtab); err != nil {
		return nil, nil, err
	}
	if err := checkFusion(cfg); err != nil {
		return nil, nil, err
	}
	if !cfg.NoExtensions && lazyExtensionShim == nil {
		if err := loadExtensionShim(cfg); err != nil {
			return nil, nil, fmt.Errorf("py: extension shim (Config.NoExtensions skips it; on Linux it needs glibc 2.28): %w", err)
		}
	}
	freeThread = call0(cPyGILDisabled) != 0
	n := cfg.Slots
	if n <= 0 {
		n = runtime.GOMAXPROCS(0)
		if !freeThread {
			n = min(n, 4)
		}
	}
	slots = make(chan struct{}, n)
	maxThreads := cfg.MaxThreads
	if maxThreads <= 0 {
		maxThreads = 4096
	}
	call1(cPySetLimit, uintptr(maxThreads))
	spares := cfg.SpareThreads
	if spares == 0 {
		spares = 4
	}
	spares = max(spares, 0)
	if runtime.GOOS == "windows" {
		spares = 0 // CPython starts its own threads there (no goroutine-backed threads)
	}
	need := maxThreads + spares + n + 256
	if prev := debug.SetMaxThreads(need); prev > need {
		debug.SetMaxThreads(prev) // keep a larger limit already set
	}

	errc := make(chan error, 1)
	rcc := make(chan int, 1)
	finalizeCh = make(chan chan error)
	go mainThread(cfg, spares, argv, errc, rcc)
	return errc, rcc, nil
}

// mainThread initializes Python on a dedicated OS thread, which keeps the main thread
// state for the life of the interpreter and finalizes it.
func mainThread(cfg Config, spares int, argv []string, errc chan<- error, rcc chan<- int) {
	runtime.LockOSThread() // never unlocked: this OS thread owns the main thread state
	call2(cPySetSpawn, purego.NewCallback(threadStart), uintptr(spares))
	setCallback()
	if err := initialize(cfg, argv); err != nil {
		errc <- err
		return
	}
	initializeNativeUnicode()
	if err := setup(cfg); err != nil {
		errc <- err
		return
	}
	call0(cPyStarted)
	mainTID.Store(osThreadID())
	if argv != nil {
		mainBusy.Store(true) // the command line holds the main thread
		state.Store(stateRunning)
		errc <- nil
		rc := int(int32(call0(cRunMain))) // runs the command line, then finalizes
		state.Store(stateFinalized)
		rcc <- rc
		return
	}
	mainTS := call0(cSaveThread)
	state.Store(stateRunning)
	errc <- nil

	var done chan error
	for done == nil {
		select {
		case done = <-finalizeCh:
		case req := <-mainCalls:
			req.done <- enter(req.fn, 0)
		}
	}
	call0(cPyStopping)
	call1(cRestoreThread, mainTS)
	drainPending()
	rc := int32(call0(cFinalizeEx))
	state.Store(stateFinalized)
	if rc != 0 {
		done <- errors.New("py: Py_FinalizeEx failed")
		return
	}
	done <- nil
}

// signalShim lets signal.signal(sig, None) restore the handler from before Python (see
// pythago_py.c): signal.signal() returns None for Go's handlers, and code that restores
// a saved handler, such as multiprocessing.forkserver, passes that None back.
const signalShim = `
def _install():
    import signal, _pythago
    original = signal.signal
    def signal_(signalnum, handler):
        if handler is None:
            previous = original(signalnum, signal.SIG_DFL)
            _pythago.restore_signal(int(signalnum))
            return previous
        return original(signalnum, handler)
    signal_.__doc__, signal_.__wrapped__ = original.__doc__, original
    signal.signal = signal_
_install()
del _install
`

// setup marks child processes started from sys.executable (through os.environ, so both
// Python's view and the C environment change), installs the signal shim, activates the
// environment, creates Config.Modules, sets the multiprocessing start method, and runs
// Config.Setup.
func setup(cfg Config) error {
	if int32(call0(cPyInstallModule)) != 0 {
		return fetchError()
	}
	if err := startFusion(); err != nil {
		return err
	}
	g, err := result(call0(cDictNew))
	if err != nil {
		return err
	}
	defer g.Close()
	if err := ExecIn(signalShim, g); err != nil {
		return err
	}
	if runtime.GOOS == "windows" && !cfg.PythonSignals {
		// Importing signal installed Python's SIGINT handler: Go handles Ctrl+C with a
		// console control handler, which the C runtime does not see, so CPython found
		// SIG_DFL. The C runtime's own console handler, registered later and called
		// first, would then take Ctrl+C from Go (os/signal). SIG_DFL makes it pass the
		// event on.
		if err := ExecIn("import signal\nsignal.signal(signal.SIGINT, signal.SIG_DFL)\n", g); err != nil {
			return err
		}
	}
	if exe, err := os.Executable(); err == nil {
		osmod, err := Import("os")
		if err != nil {
			return err
		}
		defer osmod.Close()
		env, err := osmod.Attr("environ")
		if err != nil {
			return err
		}
		defer env.Close()
		if err := env.SetItem(executableEnv, exe); err != nil {
			return err
		}
	}
	if cfg.Environment != "" {
		if err := activateEnvironment(cfg.Environment); err != nil {
			return err
		}
	}
	for name, attrs := range cfg.Modules {
		m, err := NewModule(name, attrs)
		if err != nil {
			return fmt.Errorf("py: module %s: %w", name, err)
		}
		m.Close()
	}
	if err := setStartMethod(cfg.MultiprocessingStart); err != nil {
		return err
	}
	if cfg.Setup != nil {
		return cfg.Setup()
	}
	return nil
}

func initialize(cfg Config, argv []string) error {
	var cargv []*byte // a python command line (Main), which preinitialization reads too
	for _, a := range argv {
		cargv = append(cargv, cString(a))
	}
	config := call2(cConfigNew, uintptr(len(cargv)), uintptr(unsafe.Pointer(unsafe.SliceData(cargv))))
	runtime.KeepAlive(cargv)
	if config == 0 {
		return errors.New("py: cannot allocate PyConfig")
	}
	defer call1(cConfigFree, config)
	var cerr *byte
	check := func(rc uintptr, what string) error {
		if int32(rc) != 0 {
			return errors.New("py: " + what + ": " + goString(cerr))
		}
		return nil
	}
	set := func(fn uintptr, v, what string) error {
		p := cString(v)
		rc := call3(fn, config, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&cerr)))
		runtime.KeepAlive(p)
		return check(rc, what)
	}
	home := cfg.Home
	if home == "" {
		home = os.Getenv("PYTHONHOME")
	}
	stdlib := stdlibSource(cfg, home)
	fsPaths := fsSearchPaths(cfg, stdlib)
	switch {
	case stdlib != nil:
		home = fsRoot
	case home == "":
		return errors.New("py: no standard library: import a pythago dist package, set Config.Stdlib, or set Config.Home or PYTHONHOME")
	}
	if err := set(cConfigSetHome, home, "home"); err != nil {
		return err
	}
	if stdlib != nil {
		for _, p := range append(fsPaths, imageSearchPaths()...) {
			if err := set(cConfigAppendSearchPath, p, "search path"); err != nil {
				return err
			}
		}
	}
	name := cfg.ProgramName
	if name == "" {
		name = os.Args[0]
	}
	if err := set(cConfigSetProgramName, name, "program name"); err != nil {
		return err
	}
	if cfg.PythonSignals {
		call2(cConfigSetSignals, config, 1)
	}
	if exe, err := os.Executable(); err == nil {
		if err := set(cConfigSetExecutable, exe, "executable"); err != nil {
			return err
		}
	}
	if argv != nil {
		rc := call4(cConfigSetArgv, config, uintptr(len(cargv)), uintptr(unsafe.Pointer(unsafe.SliceData(cargv))), uintptr(unsafe.Pointer(&cerr)))
		runtime.KeepAlive(cargv)
		if err := check(rc, "command line"); err != nil {
			return err
		}
	}
	for _, p := range cfg.SearchPaths {
		if err := set(cConfigAppendSearchPath, p, "search path"); err != nil {
			return err
		}
	}
	if err := check(call2(cInitializeCore, config, uintptr(unsafe.Pointer(&cerr))), "initialization"); err != nil {
		return err
	}
	// Before the main phase, which may start tracemalloc (see installFusion).
	if err := installFusion(cfg); err != nil {
		return err
	}
	if !cfg.NoExtensions && lazyExtensionShim != nil {
		if err := lazyExtensionShim(cfg); err != nil {
			return fmt.Errorf("py: extension shim (Config.NoExtensions skips it): %w", err)
		}
	}
	if len(mounts) > 0 {
		app := ""
		if cfg.Files != nil {
			app = fsPaths[0]
		}
		if err := installFSImporter(app); err != nil {
			return fmt.Errorf("py: installing the fs.FS importer: %w", err)
		}
	}
	if err := check(call2(cInitializeMain, config, uintptr(unsafe.Pointer(&cerr))), "initialization"); err != nil {
		return err
	}
	if runtime.GOOS == "windows" && len(mounts) > 0 {
		// site.py made the sys.path entries absolute: on Windows /pythago-fs/x became
		// Z:\pythago-fs\x, after the current drive. Imports still work (the path hook
		// normalizes), but the entries are the virtual paths again for code reading them.
		if err := Exec(windowsSysPathSrc); err != nil {
			return err
		}
	}
	// With the stdlib on disk, the application file system is not among the configured
	// search paths: the frozen encodings put it first on sys.path (see installFrozen).
	return nil
}

const windowsSysPathSrc = `
def _fix():
    import sys
    for i, p in enumerate(sys.path):
        q = p.replace("\\", "/")
        if q[1:3] == ":/":
            q = q[2:]
        if q == "/pythago-fs" or q.startswith("/pythago-fs/"):
            sys.path[i] = q
_fix()
del _fix
`

// Finalize waits for goroutines inside Do to leave, then finalizes the interpreter:
// Python joins its non-daemon threads and runs atexit handlers. Daemon threads still
// running are parked forever.
func Finalize() error {
	if !state.CompareAndSwap(stateRunning, stateFinalizing) {
		return ErrNotRunning
	}
	close(closing)
	inflight.Lock() // wait for every outer Do to return
	defer inflight.Unlock()
	done := make(chan error)
	finalizeCh <- done
	return <-done
}

// Slots returns the number of admission slots ([Config.Slots]).
func Slots() int { return cap(slots) }

// FreeThreaded reports whether the interpreter is a free-threaded (no-GIL) build.
func FreeThreaded() bool { return freeThread }
