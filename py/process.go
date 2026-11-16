package py

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"unsafe"
)

var image atomic.Uintptr

// RegisterImage records the address of a native image's pythago_symtab, the default for
// [Config.Symtab]. Generated dist packages call it from init, so importing one is enough.
func RegisterImage(symtab uintptr) { image.Store(symtab) }

type exportTable struct {
	names  []string
	lookup func(string) (uintptr, bool)
}

var exports atomic.Pointer[exportTable]

// RegisterExports adds the C API of a native image built from a shared libpython (bao
// -linux-shared-object), whose pythago_symtab lists only the glue: names are its exports
// and lookup returns an export's address. Generated dist packages call it from init with
// bao's SharedObjectExports and SharedObjectExport.
func RegisterExports(names []string, lookup func(string) (uintptr, bool)) {
	exports.Store(&exportTable{names, lookup})
}

// Run is the entry point of a program that embeds Python. In a process that Python
// started as sys.executable (a multiprocessing worker, subprocess.run([sys.executable,
// ...])) it runs the python command line with cfg. Otherwise it initializes the
// interpreter, runs fn, and finalizes. It does not return: the process exits with status
// 0; a SystemExit's code if fn returns one; 1 if fn or finalization failed otherwise (a
// Python error is printed with its traceback); or the command line's status.
func Run(cfg Config, fn func() error) {
	if IsPythonCommand() {
		os.Exit(Main(cfg, os.Args))
	}
	if err := Init(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	rc := 0
	if err := fn(); err != nil {
		rc = exitStatus(err)
	}
	if err := Finalize(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		rc = 1
	}
	os.Exit(rc)
}

// exitStatus is the process status for fn's error: a SystemExit's code, else 1 after
// printing the error (a Python error with its traceback).
func exitStatus(err error) int {
	var pe *Error
	if errors.As(err, &pe) {
		if status, ok := pe.SystemExit(); ok {
			if pe.exitPrint {
				fmt.Fprintln(os.Stderr, pe.Message)
			}
			return status
		}
	}
	printError(err)
	return 1
}

func printError(err error) {
	var pe *Error
	if errors.As(err, &pe) && pe.Traceback != "" {
		fmt.Fprint(os.Stderr, pe.Traceback)
		return
	}
	fmt.Fprintln(os.Stderr, err)
}

// ErrMainBusy is returned by OnMain under [Main], whose command line occupies the main
// thread.
var ErrMainBusy = errors.New("py: the main thread is running the python command line")

type mainCall struct {
	fn   func() error
	done chan error
}

var (
	mainCalls = make(chan mainCall)
	mainTID   atomic.Int64 // OS thread id of Python's main thread
	mainBusy  atomic.Bool
)

// OnMain runs fn on Python's main thread, with the main thread state attached. Some
// Python APIs work only there: signal.signal, and servers or event loops that install
// signal handlers. Calls from different goroutines run one at a time; inside fn, Do
// nests as usual. From fn itself, OnMain runs the nested function directly.
func OnMain(fn func() error) error {
	if mainTID.Load() == osThreadID() {
		// Already on the main thread, which only its own locked goroutine runs on.
		return enter(fn, 0)
	}
	if mainBusy.Load() {
		return ErrMainBusy
	}
	if state.Load() != stateRunning {
		return ErrNotRunning
	}
	inflight.RLock() // Finalize waits for us, like for Do
	defer inflight.RUnlock()
	if state.Load() != stateRunning {
		return ErrNotRunning
	}
	req := mainCall{fn: fn, done: make(chan error, 1)}
	select {
	case mainCalls <- req:
	case <-closing:
		return ErrNotRunning
	}
	return <-req.done
}

// activateEnvironment adds a venv-like environment: site-packages for this Python
// version and ABI through site.addsitedir, bin/ first on PATH, sys.prefix and
// VIRTUAL_ENV. On Windows these are Lib\site-packages and Scripts\, as venv makes them.
const activateEnvironmentSrc = `
def _activate(env):
    import os, site, sys
    env = os.path.abspath(env)
    lib = "python%d.%d%s" % (sys.version_info[0], sys.version_info[1], getattr(sys, "abiflags", ""))
    sp = os.path.join(env, "lib", lib, "site-packages")
    if sys.platform == "win32":
        sp = os.path.join(env, "Lib", "site-packages")
    if not os.path.isdir(sp):
        raise FileNotFoundError("environment %s has no %s (wrong Python version or ABI?)" % (env, sp))
    try:
        with open(os.path.join(env, "pyvenv.cfg"), encoding="utf-8") as f:
            cfg = dict(l.split("=", 1) for l in f.read().splitlines() if "=" in l)
        version = {k.strip(): v.strip() for k, v in cfg.items()}.get("version", "")
    except FileNotFoundError:
        version = ""
    want = "%d.%d" % sys.version_info[:2]
    if version and version != want and not version.startswith(want + "."):
        raise RuntimeError("environment %s was created for Python %s, not %s" % (env, version, want))
    site.addsitedir(sp)
    bin_ = os.path.join(env, "Scripts" if sys.platform == "win32" else "bin")
    if os.path.isdir(bin_):
        os.environ["PATH"] = bin_ + os.pathsep + os.environ.get("PATH", os.defpath)
    os.environ["VIRTUAL_ENV"] = env
    sys.prefix = sys.exec_prefix = env
`

func activateEnvironment(env string) error {
	g, err := result(call0(cDictNew))
	if err != nil {
		return err
	}
	defer g.Close()
	if err := ExecIn(activateEnvironmentSrc, g); err != nil {
		return err
	}
	f, err := g.Item("_activate")
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := f.Call(env)
	r.Close()
	return err
}

// setStartMethod changes multiprocessing's default start method without fixing it, so
// code can still call multiprocessing.set_start_method. 3.14 already defaults to
// forkserver on Linux, and needs no import at startup; macOS defaults to spawn.
const startMethodSrc = `
def _set(method):
    import sys
    if method == "forkserver" and sys.version_info >= (3, 14) and sys.platform == "linux":
        return
    from multiprocessing import context
    context._default_context._default_context = context._concrete_contexts[method]
`

func setStartMethod(method string) error {
	switch method {
	case "default":
		return nil
	case "":
		if runtime.GOOS == "windows" {
			return nil // spawn, the only start method there
		}
		method = "forkserver"
	case "fork", "spawn", "forkserver":
	default:
		return fmt.Errorf("py: unknown multiprocessing start method %q", method)
	}
	g, err := result(call0(cDictNew))
	if err != nil {
		return err
	}
	defer g.Close()
	if err := ExecIn(startMethodSrc, g); err != nil {
		return err
	}
	f, err := g.Item("_set")
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := f.Call(method)
	r.Close()
	return err
}

// ImageInfo reports the Python version ("3.13") and variant of the native image that
// [Init] would use (Config.Symtab defaults to the registered image), without
// initializing it, so an environment can be prepared for it first.
func ImageInfo() (version string, freeThreaded bool, err error) {
	symtab, err := loadImage()
	if err != nil {
		return "", false, err
	}
	if symtab == 0 {
		return "", false, errors.New("py: no native image registered")
	}
	if err := resolve(symtab); err != nil {
		return "", false, err
	}
	v := *(*uint32)(unsafe.Pointer(symbol("Py_Version")))
	return fmt.Sprintf("%d.%d", v>>24, v>>16&0xff), call0(cPyGILDisabled) != 0, nil
}
