package py_test

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
	"unsafe"

	_ "github.com/moriyoshi/pythago/internal/testimage" // registers the native image and stdlib
	"github.com/moriyoshi/pythago/internal/testwatch"
	"github.com/moriyoshi/pythago/py"
)

// The tests run against the test image in .work/testimage (module path
// github.com/moriyoshi/pythago/internal/testimage, joined by .work/go.work), which
// ci/lib.sh generates with cmd/pythago-dist (GIL or free-threaded, whichever was generated
// last), with its embedded standard library; set PYTHONHOME to use an installation on disk
// instead.
// testConfig is shared with the processes the tests start from sys.executable, which
// run TestMain again and dispatch to py.Main.
func testConfig() py.Config {
	return py.Config{
		Environment: os.Getenv("PYTHAGO_TEST_ENV"),
		Modules: map[string]map[string]any{
			"gocfg": {
				"triple": py.Func(func(args []*py.Object, _ py.Kwargs) (any, error) {
					n, err := args[0].Int64()
					return 3 * n, err
				}),
				"keep":  py.Func(keepArg),
				"probe": py.Func(probeKept),
				"debug_hooks": py.Func(func([]*py.Object, py.Kwargs) (any, error) {
					return py.DebugHooks(), nil
				}),
			},
			"_fusiontest": py.FusionTestModule(),
		},
		CheckBorrows:       os.Getenv("PYTHAGO_TEST_CHECK_BORROWS") == "1",
		PythonSignals:      os.Getenv("PYTHAGO_TEST_PYSIGNALS") == "1",
		ExtensionShimDir:   os.Getenv("PYTHAGO_TEST_EXTSHIM_DIR"),
		ExtensionShimMemfd: os.Getenv("PYTHAGO_TEST_EXTSHIM_MEMFD") == "1",
		Files:              appFiles,
		FusedBytes:         testFusedBytes(),
	}
}

// testFusedBytes is Config.FusedBytes for the tests: PYTHAGO_TEST_FUSION=1 runs them with
// the fusion on, 16 MiB (inherited by sys.executable children).
func testFusedBytes() int {
	if os.Getenv("PYTHAGO_TEST_FUSION") == "1" {
		return 16 << 20
	}
	return 0
}

// appFiles is application code served from a Go file system (Config.Files).
var appFiles = fstest.MapFS{
	"appmod/__init__.py": {Data: []byte("def greet(name):\n    return 'hello ' + name\n\n" +
		"def fail():\n    raise ValueError('from appmod')\n")},
	"appmod/helper.py": {Data: []byte("X = 42\n")},
	"appmod/data.txt":  {Data: []byte("package data\n")},
	"nsapp/part.py":    {Data: []byte("NAME = 'namespace portion'\n")},
}

// makeTestEnv creates a venv-like environment for both ABIs the tests run on.
func makeTestEnv(dir string) error {
	pth := filepath.Join(dir, "pthdir")
	files := map[string]string{
		"pthdir/pthmod.py":       "VALUE = 'from .pth'\n",
		"bin/pythago-env-tool":   "#!/bin/sh\necho env tool\n",
		"site/envmod.py":         "VALUE = 'from site-packages'\n",
		"site/pythago-extra.pth": pth + "\n",
	}
	if runtime.GOOS == "windows" { // venv's layout there: Scripts\, Lib\site-packages
		delete(files, "bin/pythago-env-tool")
		files["Scripts/pythago-env-tool.bat"] = "@echo env tool\r\n"
	}
	for _, lib := range []string{"python3.13", "python3.14t"} {
		sp := filepath.Join(dir, "lib", lib, "site-packages")
		if runtime.GOOS == "windows" {
			sp = filepath.Join(dir, "Lib", "site-packages")
		}
		if err := os.MkdirAll(sp, 0o755); err != nil {
			return err
		}
		for name, body := range files {
			path := filepath.Join(dir, name)
			if strings.HasPrefix(name, "site/") {
				path = filepath.Join(sp, strings.TrimPrefix(name, "site/"))
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

// The tests run against the test image in .work/testimage (see testConfig); set
// PYTHONHOME to use an installation on disk instead.
func TestMain(m *testing.M) {
	if py.IsPythonCommand() { // started by the tests below as sys.executable
		os.Exit(py.Main(testConfig(), os.Args))
	}
	env, err := os.MkdirTemp("", "pythago-py-test-env-")
	if err == nil {
		err = makeTestEnv(env)
	}
	if err != nil {
		fmt.Println("test environment:", err)
		os.Exit(1)
	}
	os.Setenv("PYTHAGO_TEST_ENV", env) // inherited by sys.executable children
	disarm := testwatch.Arm("py.Init") // -test.timeout covers only m.Run
	if err := py.Init(testConfig()); err != nil {
		fmt.Println("Init:", err)
		os.Exit(1)
	}
	disarm()
	code := m.Run()
	testwatch.Arm("py.Finalize") // armed through os.Exit
	if err := py.Finalize(); err != nil {
		fmt.Println("Finalize:", err)
		code = 1
	}
	os.RemoveAll(env)
	os.Exit(code)
}

func do(t *testing.T, fn func() error) {
	t.Helper()
	if err := py.Do(fn); err != nil {
		t.Fatal(err)
	}
}

func eval(t *testing.T, expr string) any {
	t.Helper()
	var v any
	do(t, func() error {
		o, err := py.Eval(expr)
		if err != nil {
			return err
		}
		defer o.Close()
		v, err = o.Go()
		return err
	})
	return v
}

func TestConvert(t *testing.T) {
	in := map[string]any{
		"list":  []any{int64(1), "x", 2.5, nil, true, []byte("b")},
		"tuple": py.Tuple{int64(-7), ""},
		"ints":  []int{1, 2},
		"big":   new(big.Int).Lsh(big.NewInt(1), 100),
		"u64":   uint64(1) << 63,
		"utf8":  "héllo 🐍",
	}
	want := map[any]any{
		"list":  []any{int64(1), "x", 2.5, nil, true, []byte("b")},
		"tuple": []any{int64(-7), ""},
		"ints":  []any{int64(1), int64(2)},
		"big":   new(big.Int).Lsh(big.NewInt(1), 100),
		"u64":   new(big.Int).Lsh(big.NewInt(1), 63),
		"utf8":  "héllo 🐍",
	}
	do(t, func() error {
		id, err := py.Eval("lambda x: x")
		if err != nil {
			return err
		}
		defer id.Close()
		r, err := id.Call(in)
		if err != nil {
			return err
		}
		defer r.Close()
		got, err := r.Go()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip:\n got %#v\nwant %#v", got, want)
		}
		if s, _ := r.Repr(); !strings.Contains(s, "'tuple': (-7, '')") {
			t.Errorf("repr %s", s)
		}
		return nil
	})
	if v := eval(t, "2**100 + 1"); v.(*big.Int).String() != "1267650600228229401496703205377" {
		t.Errorf("2**100+1 = %v", v)
	}
	if v := eval(t, "bytearray(b'ab')"); !reflect.DeepEqual(v, []byte("ab")) {
		t.Errorf("bytearray = %v", v)
	}
}

func TestError(t *testing.T) {
	err := py.Do(func() error {
		return py.Exec("def f():\n    raise ValueError('bad value')\nf()\n")
	})
	var pe *py.Error
	if !errors.As(err, &pe) {
		t.Fatalf("got %v, want *py.Error", err)
	}
	if pe.Type != "ValueError" || pe.Message != "bad value" {
		t.Errorf("Type %q Message %q", pe.Type, pe.Message)
	}
	if !strings.Contains(pe.Traceback, "line 2, in f") || !strings.Contains(pe.Traceback, "ValueError: bad value") {
		t.Errorf("traceback:\n%s", pe.Traceback)
	}
	err = py.Do(func() error { return py.Exec("import json\njson.loads('{')") })
	if !errors.As(err, &pe) || pe.Type != "json.decoder.JSONDecodeError" {
		t.Errorf("got %v (%q)", err, pe.Type)
	}
}

var setupModule sync.Once

// gomod is a Go-defined module shared by the tests.
func module(t *testing.T) {
	setupModule.Do(func() {
		do(t, func() error {
			m, err := py.NewModule("gomod", map[string]any{
				"version": "1",
				"add": py.Func(func(args []*py.Object, _ py.Kwargs) (any, error) {
					var sum int64
					for _, a := range args {
						n, err := a.Int64()
						if err != nil {
							return nil, err
						}
						sum += n
					}
					return sum, nil
				}),
				"kw": py.Func(func(args []*py.Object, kw py.Kwargs) (any, error) {
					out := map[string]any{"nargs": len(args)}
					for k, v := range kw.Each {
						out[k] = v // a borrowed argument, converted (a new reference) by From
					}
					return out, nil
				}),
				"fail": py.Func(func([]*py.Object, py.Kwargs) (any, error) {
					return nil, errors.New("failed in Go")
				}),
				"boom": py.Func(func([]*py.Object, py.Kwargs) (any, error) {
					panic("boom")
				}),
				"call": py.Func(func(args []*py.Object, _ py.Kwargs) (any, error) {
					return args[0].Call() // a Python exception passes through unchanged
				}),
				"tid": py.Func(func([]*py.Object, py.Kwargs) (any, error) {
					return threadID(), nil
				}),
				// nap blocks in Go with the thread state released.
				"nap": py.Func(func(args []*py.Object, _ py.Kwargs) (any, error) {
					ms, err := args[0].Int64()
					if err != nil {
						return nil, err
					}
					py.Release(func() { time.Sleep(time.Duration(ms) * time.Millisecond) })
					return nil, nil
				}),
				// nested enters Python again from inside a Go function, also after Release.
				"nested": py.Func(func([]*py.Object, py.Kwargs) (any, error) {
					var a, b any
					if err := py.Do(func() error {
						o, err := py.Eval("6 * 7")
						if err != nil {
							return err
						}
						defer o.Close()
						a, err = o.Go()
						return err
					}); err != nil {
						return nil, err
					}
					var err error
					py.Release(func() {
						err = py.Do(func() error {
							o, err := py.Eval("'x' * 3")
							if err != nil {
								return err
							}
							defer o.Close()
							b, err = o.Go()
							return err
						})
					})
					return []any{a, b}, err
				}),
			})
			if err != nil {
				return err
			}
			m.Close()
			return nil
		})
	})
}

func TestFunc(t *testing.T) {
	module(t)
	do(t, func() error {
		return py.Exec(`
import gomod
assert gomod.version == "1"
assert gomod.add(1, 2, 3) == 6
assert gomod.kw(1, a=2, b="x") == {"nargs": 1, "a": 2, "b": "x"}, gomod.kw(1, a=2, b="x")
try:
    gomod.fail()
except RuntimeError as e:
    assert str(e) == "failed in Go", e
else:
    raise AssertionError("no exception")
try:
    gomod.boom()
except RuntimeError as e:
    assert "panicked: boom" in str(e), e
else:
    raise AssertionError("no exception")
try:
    gomod.add(1, "x")
except TypeError:
    pass
else:
    raise AssertionError("no TypeError")
def raiser():
    raise KeyError("k")
try:
    gomod.call(raiser)
except KeyError:
    pass
else:
    raise AssertionError("no KeyError")
assert gomod.nested() == [42, "xxx"]
`)
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPythonThreads(t *testing.T) {
	module(t)
	before := py.ReadStats()
	do(t, func() error {
		return py.Exec(`
import threading, gomod, concurrent.futures as cf
seen = {}
def probe(i):
    # The Go function runs on the Python thread's own OS thread.
    seen[i] = (threading.get_ident(), threading.get_native_id(), gomod.tid())
ts = [threading.Thread(target=probe, args=(i,)) for i in range(32)]
[t.start() for t in ts]; [t.join() for t in ts]
assert all(seen[i][0] == ts[i].ident and seen[i][1] == ts[i].native_id == seen[i][2] for i in range(32)), seen
assert len({v[1] for v in seen.values()}) > 1
with cf.ThreadPoolExecutor(8) as ex:
    assert sum(ex.map(lambda x: gomod.add(x, x), range(1000))) == 2 * sum(range(1000))
import _thread
done = _thread.allocate_lock(); done.acquire()
_thread.start_new_thread(lambda: (gomod.add(1), done.release()), ())  # detached
assert done.acquire(timeout=5)
local = threading.local()
local.x = "main"
def other():
    assert not hasattr(local, "x")
t = threading.Thread(target=other); t.start(); t.join()
`)
	})
	after := py.ReadStats()
	if runtime.GOOS == "windows" {
		return // CPython starts its own threads there; none is goroutine-backed
	}
	if n := after.ThreadsStarted - before.ThreadsStarted; n < 33 {
		t.Errorf("%d goroutine-backed threads started, want >= 33", n)
	}
	waitFor(t, "threads to exit", func() bool { return py.ReadStats().Threads == before.Threads })
}

func TestThreadLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CPython starts its own threads on Windows: py's cap does not apply")
	}
	prev := py.SetThreadLimit(int(py.ReadStats().Threads) + 2)
	defer py.SetThreadLimit(prev)
	do(t, func() error {
		return py.Exec(`
import threading
ev = threading.Event()
ts = [threading.Thread(target=ev.wait) for _ in range(3)]
ts[0].start(); ts[1].start()
try:
    ts[2].start()
except RuntimeError as e:
    assert "can't start new thread" in str(e), e
else:
    raise AssertionError("third thread started")
ev.set(); ts[0].join(); ts[1].join()
`)
	})
}

func TestGoroutines(t *testing.T) {
	do(t, func() error {
		return py.Exec("import threading\ncounter = [0]\ncounter_lock = threading.Lock()\n" +
			"def bump():\n    with counter_lock:\n        counter[0] += 1\n")
	})
	base := osThreads()
	var peak atomic.Int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				if n := int64(osThreads()); n > peak.Load() {
					peak.Store(n)
				}
			}
		}
	}()
	const goroutines, calls = 2000, 5
	var wg sync.WaitGroup
	var failed atomic.Int32
	for range goroutines {
		wg.Go(func() {
			for range calls {
				if err := py.Do(func() error { return py.Exec("bump()") }); err != nil {
					t.Error(err)
					failed.Add(1)
					return
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	if n := eval(t, "counter[0]"); n != int64(goroutines*calls) {
		t.Errorf("counter %v, want %d", n, goroutines*calls)
	}
	// Admission bounds the OS threads Go→Python calls occupy: one per slot, plus up to
	// GOMAXPROCS threads running the other goroutines while those are in C.
	if limit := int64(base + py.Slots() + runtime.GOMAXPROCS(0) + 8); peak.Load() > limit {
		t.Errorf("peak OS threads %d, want <= %d", peak.Load(), limit)
	}
	t.Logf("OS threads: base %d, peak %d", base, peak.Load())
}

func TestRelease(t *testing.T) {
	module(t)
	// While one goroutine sleeps in Go inside a Python call, others still run Python.
	napping := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- py.Do(func() error {
			close(napping)
			return py.Exec("import gomod\ngomod.nap(300)")
		})
	}()
	<-napping
	start := time.Now()
	for range 20 {
		do(t, func() error { return py.Exec("sum(range(1000))") })
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("Python calls took %v while another call slept with its thread state released", d)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Python threads and goroutines calling Go functions that re-enter Python, with every
// admission slot taken by outer calls.
func TestNestedUnderLoad(t *testing.T) {
	module(t)
	var wg sync.WaitGroup
	for range 4 * runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			if err := py.Do(func() error {
				return py.Exec("import gomod\nfor _ in range(20):\n    r = gomod.nested()\n    assert r == [42, 'xxx'], r")
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// mainThreadID is the main OS thread's id: package initialization runs on it.
var mainThreadID = threadID()

// TestThreadStateFreedAtThreadExit: when an OS thread that ran Do exits, its thread
// state is deleted (a pthread key destructor; on Windows a fiber-local slot's callback).
//   - The goroutine must not be on the main thread, which Go wedges instead of exiting
//     (runtime.mexit): one that lands there is held until the test ends, so that the
//     next attempt gets another thread.
//   - It failed rarely, on Windows only (arm64 gil, amd64 Server Core ft), after 5 s and
//     with no cause known: the wait is 30 s, a deletion that took more than 5 s is
//     reported on stderr, and a failure says whether the OS thread still exists.
func TestThreadStateFreedAtThreadExit(t *testing.T) {
	before := py.ReadStats()
	hold := make(chan struct{})
	defer close(hold)
	tid := -1
	for attempt := 0; tid == -1; attempt++ {
		if attempt == 3 {
			t.Fatal("three goroutines in a row ran on the main thread")
		}
		got := make(chan int)
		go func() {
			runtime.LockOSThread() // never unlocked here: the OS thread exits with the goroutine
			id := threadID()
			if id == mainThreadID {
				got <- -1
				<-hold
				runtime.UnlockOSThread()
				return
			}
			if err := py.Do(func() error { return py.Exec("x = 1") }); err != nil {
				t.Error(err)
			}
			got <- id
		}()
		tid = <-got
	}
	start := time.Now()
	for py.ReadStats().ThreadStatesDeleted <= before.ThreadStatesDeleted {
		if time.Since(start) > 30*time.Second {
			s := py.ReadStats()
			t.Fatalf("OS thread %d's thread state was not deleted 30 s after its goroutine exited "+
				"(thread still exists: %s; thread states created %d -> %d, deleted %d -> %d)",
				tid, threadAlive(tid), before.ThreadStatesCreated, s.ThreadStatesCreated,
				before.ThreadStatesDeleted, s.ThreadStatesDeleted)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d := time.Since(start); d > 5*time.Second {
		fmt.Fprintf(os.Stderr, "TestThreadStateFreedAtThreadExit: OS thread %d's thread state was deleted %v after its goroutine exited\n", tid, d)
	}
}

func TestUnclosedObjectsReleased(t *testing.T) {
	do(t, func() error {
		return py.Exec("import weakref\nclass Box: pass\ndead = []\n")
	})
	do(t, func() error {
		o, err := py.Eval("(lambda b: (weakref.finalize(b, dead.append, 1), b)[1])(Box())")
		if err != nil {
			return err
		}
		_ = o // unreachable after this function returns; never closed
		return nil
	})
	waitFor(t, "an unclosed object to be released", func() bool {
		runtime.GC()
		return eval(t, "len(dead)") == int64(1)
	})
}

// Goroutines and Python threads share objects while Python threads start on OS threads
// that Do used before.
func TestMixedThreads(t *testing.T) {
	module(t)
	do(t, func() error {
		return py.Exec(`
import threading, gomod
shared = {"items": [], "lock": threading.Lock()}
def worker(n):
    for i in range(200):
        with shared["lock"]:
            shared["items"].append((n, i, [i] * 3))
            if len(shared["items"]) > 100:
                shared["items"].pop(0)
        gomod.add(i, n)
def spawn(g):
    ts = [threading.Thread(target=worker, args=(g,)) for _ in range(2)]
    [t.start() for t in ts]
    worker(-1)
    [t.join() for t in ts]
`)
	})
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for range 10 {
				if err := py.Do(func() error {
					return py.Exec(fmt.Sprintf("spawn(%d)", g))
				}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// UTF-8 mode: text I/O does not depend on the (C) locale of an isolated configuration.
func TestUTF8Mode(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import locale, sys, tempfile
assert sys.flags.utf8_mode == 1, sys.flags
assert locale.getpreferredencoding(False).lower() in ("utf-8", "utf8"), locale.getpreferredencoding(False)
with tempfile.TemporaryFile("w+") as f:
    f.write("héllo ✓"); f.seek(0); assert f.read() == "héllo ✓"
`)
	})
}

// sys.executable runs as the python command line in a child process.
func TestSysExecutable(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import subprocess, sys
r = subprocess.run([sys.executable, "-c", "import sys; print(sys.argv[1:], 6 * 7)", "a"],
                   capture_output=True, text=True, timeout=60)
assert r.returncode == 0 and r.stdout.strip() == "['a'] 42", (r.returncode, r.stdout, r.stderr)
r = subprocess.run([sys.executable, "-c", "raise SystemExit(3)"], timeout=60)
assert r.returncode == 3, r.returncode
`)
	})
}

// -X tracemalloc[=N] on the python command line starts tracemalloc at initialization, as
// in stock python -I; PYTHONTRACEMALLOC is ignored, as in python -I, and an invalid
// frame count fails initialization.
func TestSysExecutableTracemalloc(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import os, subprocess, sys
code = "import tracemalloc; print(tracemalloc.is_tracing(), tracemalloc.get_traceback_limit())"
def run(*args, env=None):
    r = subprocess.run([sys.executable, *args, "-c", code], capture_output=True, text=True,
                       timeout=60, env=env)
    return r.returncode, r.stdout.strip(), r.stderr
for args, want in [(["-X", "tracemalloc"], "True 1"), (["-X", "tracemalloc=7"], "True 7"), ([], "False 1")]:
    rc, out, err = run(*args)
    assert rc == 0 and out == want, (args, rc, out, err, want)
rc, out, err = run(env=dict(os.environ, PYTHONTRACEMALLOC="5"))
assert rc == 0 and out == "False 1", ("PYTHONTRACEMALLOC", rc, out, err)
rc, out, err = run("-X", "tracemalloc=x")
assert rc == 1 and "invalid number of frames" in err, ("invalid", rc, out, err)
`)
	})
}

// -X dev, -X faulthandler, -X perf and -X perf_jit, and -X int_max_str_digits on the
// python command line apply as in stock python -I (an isolated configuration fixes them
// otherwise), and their PYTHON* variables stay ignored. Dev mode comes with the
// allocators' debug hooks, faulthandler and the "default" warning filter; the perf
// trampoline is active where CPython has one; an invalid digit limit fails
// initialization. Every case is checked and all failures reported together.
func TestSysExecutableXOptions(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import ast, glob, os, subprocess, sys
errors = []
def run(*args, code, env=None):
    r = subprocess.run([sys.executable, *args, "-c", code], capture_output=True, text=True,
                       timeout=60, env=env)
    return r.returncode, r.stdout.strip(), r.stderr
def check(what, got, want):
    if got != want:
        errors.append((what, got, want))
def env(**kw):
    return dict(os.environ, **kw)

dev = "import sys, faulthandler, gocfg; print(repr((sys.flags.dev_mode, faulthandler.is_enabled(), sys.warnoptions, gocfg.debug_hooks())))"
def devrun(*args, env=None):
    rc, out, err = run(*args, code=dev, env=env)
    return rc, ast.literal_eval(out) if rc == 0 else err
check("no -X dev", devrun(), (0, (False, False, [], False)))
check("-X dev", devrun("-X", "dev"), (0, (True, True, ["default"], True)))
check("PYTHONDEVMODE", devrun(env=env(PYTHONDEVMODE="1")), (0, (False, False, [], False)))
check("-X faulthandler", devrun("-X", "faulthandler"), (0, (False, True, [], False)))
check("PYTHONFAULTHANDLER", devrun(env=env(PYTHONFAULTHANDLER="1")), (0, (False, False, [], False)))

digits = "import sys; print(sys.flags.int_max_str_digits, sys.get_int_max_str_digits())"
check("no -X int_max_str_digits", run(code=digits)[:2], (0, "4300 4300"))
check("-X int_max_str_digits=640", run("-X", "int_max_str_digits=640", code=digits)[:2], (0, "640 640"))
check("-X int_max_str_digits=0", run("-X", "int_max_str_digits=0", code=digits)[:2], (0, "0 0"))
check("PYTHONINTMAXSTRDIGITS", run(code=digits, env=env(PYTHONINTMAXSTRDIGITS="640"))[:2], (0, "4300 4300"))
rc, out, err = run("-X", "int_max_str_digits=10", code="pass")
check("-X int_max_str_digits=10", (rc, "invalid limit" in err), (1, True))

def perf(*args, code="import os, sys; print(os.getpid(), sys.is_stack_trampoline_active())", env=None):
    rc, out, err = run(*args, code=code, env=env)
    pid, _, active = out.partition(" ")
    if pid.isdigit():  # the trampoline's files (Linux)
        for p in [f"/tmp/perf-{pid}.map", f"/tmp/jit-{pid}.dump", *glob.glob(f"/tmp/jitted-{pid}-*.so")]:
            try:
                os.remove(p)
            except FileNotFoundError:
                pass
    return rc, active, err
# Whether this CPython has the trampoline: activating it at run time works.
rc, supported, err = perf(code="import os, sys\ntry:\n    sys.activate_stack_trampoline('perf')\n    sys.deactivate_stack_trampoline()\n    print(os.getpid(), True)\nexcept ValueError:\n    print(os.getpid(), False)")
check("trampoline support probe", rc, 0)
check("no -X perf", perf()[:2], (0, "False"))
check("-X perf", perf("-X", "perf")[:2], (0, supported))
check("-X perf_jit", perf("-X", "perf_jit")[:2], (0, supported))
check("PYTHONPERFSUPPORT", perf(env=env(PYTHONPERFSUPPORT="1"))[:2], (0, "False"))
assert not errors, errors
`)
	})
}

func TestMultiprocessingSpawn(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import multiprocessing as mp
with mp.get_context("spawn").Pool(2) as pool:
    assert pool.map(abs, [-1, -2, -3]) == [1, 2, 3]
`)
	})
}

func TestMultiprocessingForkserver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no forkserver on Windows")
	}
	do(t, func() error {
		return py.Exec(`
import multiprocessing as mp
with mp.get_context("forkserver").Pool(2) as pool:
    assert pool.map(abs, [-1, -2, -3]) == [1, 2, 3]
`)
	})
}

// signal.signal() reports Go's handlers as None; passing that None back restores them.
// signal.signal works only on Python's main thread (py's init thread, not Do's), so this
// runs in a sys.executable child, where the command line runs on the main thread.
func TestSignalRestore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go installs no C signal handlers on Windows: nothing to restore")
	}
	do(t, func() error {
		return py.Exec(`
import subprocess, sys
code = """
import signal
old = signal.signal(signal.SIGCHLD, signal.SIG_IGN)
assert old is None, old  # installed by Go, not by Python
assert signal.signal(signal.SIGCHLD, old) == signal.SIG_IGN
"""
r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=60)
assert r.returncode == 0, r.stderr
`)
	})
}

// A forked child (os.fork, multiprocessing "fork") cannot run Go: threads start with libc
// and Go functions raise.
func TestForkChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no fork on Windows")
	}
	module(t)
	do(t, func() error {
		return py.Exec(`
import os, threading, gomod
pid = os.fork()
if pid == 0:
    ok = True
    try:
        out = []
        th = threading.Thread(target=out.append, args=(1,)); th.start(); th.join()
        ok = ok and out == [1]
        try:
            gomod.add(1, 2)
            ok = False
        except RuntimeError as e:
            ok = ok and "forked child" in str(e)
    except BaseException:
        ok = False
    os._exit(0 if ok else 1)
_, status = os.waitpid(pid, 0)
assert os.waitstatus_to_exitcode(status) == 0, status
assert gomod.add(1, 2) == 3  # the parent is unaffected
`)
	})
}

// SystemExit carries python's exit status (py.Run exits with it).
func TestSystemExit(t *testing.T) {
	for src, want := range map[string]int{"raise SystemExit(3)": 3, "import sys; sys.exit()": 0, "import sys; sys.exit('bye')": 1} {
		err := py.Do(func() error { return py.Exec(src) })
		var pe *py.Error
		if !errors.As(err, &pe) {
			t.Fatalf("%s: got %v", src, err)
		}
		if status, ok := pe.SystemExit(); !ok || status != want {
			t.Errorf("%s: SystemExit() = %d, %v; want %d, true", src, status, ok, want)
		}
	}
	err := py.Do(func() error { return py.Exec("raise ValueError") })
	var pe *py.Error
	if errors.As(err, &pe) {
		if _, ok := pe.SystemExit(); ok {
			t.Error("ValueError reported as SystemExit")
		}
	}
}

// Config.Modules exist in processes started from sys.executable too.
func TestModulesInChildren(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import gocfg, multiprocessing as mp
assert gocfg.triple(2) == 6
with mp.get_context("spawn").Pool(1) as pool:
    assert pool.apply(eval, ("__import__('gocfg').triple(5)",)) == 15
`)
	})
}

// OnMain runs on Python's main thread, where signal.signal works; Do does not.
func TestOnMain(t *testing.T) {
	const src = "import signal, threading\n" +
		"assert threading.current_thread() is threading.main_thread()\n" +
		"sig = getattr(signal, 'SIGUSR1', signal.SIGTERM)\n" +
		"old = signal.signal(sig, lambda *a: None)\n" +
		"signal.signal(sig, old)\n"
	if err := py.OnMain(func() error {
		if err := py.Exec(src); err != nil {
			return err
		}
		return py.OnMain(func() error { return py.Exec("x = 1") }) // nested: runs directly
	}); err != nil {
		t.Fatal(err)
	}
	err := py.Do(func() error { return py.Exec(src) })
	var pe *py.Error
	if !errors.As(err, &pe) || pe.Type != "AssertionError" && pe.Type != "ValueError" {
		t.Errorf("Do on a non-main thread: got %v, want an error", err)
	}
}

func TestEnvironment(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import os, shutil, sys, envmod, pthmod
env = os.environ["PYTHAGO_TEST_ENV"]
assert envmod.VALUE == "from site-packages" and pthmod.VALUE == "from .pth"
assert sys.prefix == env and os.environ["VIRTUAL_ENV"] == env, (sys.prefix, env)
if sys.platform == "win32":
    assert shutil.which("pythago-env-tool").lower() == os.path.join(env, "Scripts", "pythago-env-tool.bat").lower()
else:
    assert shutil.which("pythago-env-tool") == os.path.join(env, "bin", "pythago-env-tool")
`)
	})
}

// multiprocessing defaults to forkserver (spawn, the only method, on Windows), but code
// can still choose a start method.
func TestMultiprocessingStartMethod(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import os, subprocess, sys
def child(code):
    r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=60)
    assert r.returncode == 0, r.stderr
    return r.stdout.strip()
assert child("import multiprocessing as mp; print(mp.get_start_method())") == ("spawn" if sys.platform == "win32" else "forkserver")
assert child("import multiprocessing as mp; mp.set_start_method('spawn'); print(mp.get_start_method())") == "spawn"
`)
	})
}

// Config.PythonSignals: Python's SIGINT handler (KeyboardInterrupt) instead of Go's.
func TestPythonSignals(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import os, subprocess, sys
code = "import signal; print(signal.getsignal(signal.SIGINT) is signal.default_int_handler)"
def child(env):
    r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=60, env=env)
    assert r.returncode == 0, r.stderr
    return r.stdout.strip()
assert child(dict(os.environ, PYTHAGO_TEST_PYSIGNALS="1")) == "True"
assert child(dict(os.environ, PYTHAGO_TEST_PYSIGNALS="0")) == "False"
`)
	})
}

// Application code and the stdlib come from Go file systems (Config.Files, the embedded
// stdlib of the dist package).
func TestFSImport(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import importlib.resources, inspect, json, os, pkgutil, sys, traceback
import appmod, appmod.helper, nsapp.part
assert appmod.greet("go") == "hello go" and appmod.helper.X == 42
assert appmod.__file__ == "/pythago-fs/app/appmod/__init__.py", appmod.__file__
assert nsapp.part.NAME == "namespace portion" and nsapp.__file__ is None  # a namespace package
try:
    appmod.fail()
except ValueError:
    tb = traceback.format_exc()
assert "/pythago-fs/app/appmod/__init__.py" in tb and "raise ValueError('from appmod')" in tb, tb
assert "def greet(name):" in inspect.getsource(appmod)
assert importlib.resources.files("appmod").joinpath("data.txt").read_text() == "package data\n"
assert pkgutil.get_data("appmod", "data.txt") == b"package data\n"
assert [m.name for m in pkgutil.iter_modules(appmod.__path__)] == ["helper"]
assert sys.path[0] == "/pythago-fs/app" or os.environ.get("PYTHONHOME"), sys.path
if not os.environ.get("PYTHONHOME"):
    assert json.__file__.startswith("/pythago-fs/lib/python3."), json.__file__
    assert type(json.__loader__).__name__ == "GoFSLoader"
`)
	})
}

// sqlite stats its files through the glue's stat64/fstat64/lstat64 wrappers (pre-2.33
// glibc ABI, kept for the 2.17 floor).
func TestSQLiteFile(t *testing.T) {
	db := filepath.Join(t.TempDir(), "t.db")
	do(t, func() error {
		return py.Exec(fmt.Sprintf(`
import os, sqlite3
con = sqlite3.connect(%q)
con.execute("create table t (x)")
con.executemany("insert into t values (?)", [(i,) for i in range(100)])
con.commit()
assert con.execute("select sum(x) from t").fetchone()[0] == 4950
con.close()
assert os.stat(%q).st_size > 0 and os.lstat(%q).st_size == os.stat(%q).st_size
`, db, db, db, db))
	})
}

// Runs last: a daemon thread still running when TestMain finalizes the interpreter.
func TestDaemonAtFinalize(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import threading, time
def forever():
    while True:
        time.sleep(0.001)
threading.Thread(target=forever, daemon=True).start()
`)
	})
}

// The extension shim makes the C API visible in the global scope, where C extensions'
// undefined symbols resolve: functions and data (the address of PyExc_SystemError's cell
// holds the SystemError type).
func TestExtensionShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		// No shim: the image's python3XY.dll, loaded by the Windows loader from the
		// extracted image, is what extension modules bind to by name. One copy of it, the
		// image's, serves the interpreter, ctypes.pythonapi and the stdlib's .pyd files.
		do(t, func() error {
			return py.Exec(`
import ctypes, os, sys, _winapi
import _socket, _ssl, _sqlite3, _ctypes, select, _hashlib, pyexpat, unicodedata
dll = _winapi.GetModuleFileName(sys.dllhandle)
image = os.path.dirname(dll)
assert os.path.basename(dll).lower().startswith("python3"), dll
assert os.path.isfile(os.path.join(image, "pythago_glue.dll")), image
for m in (_socket, _ssl, _sqlite3, _ctypes, select, _hashlib, pyexpat, unicodedata):
    assert os.path.dirname(m.__file__).lower() == os.path.join(image, "DLLs").lower(), (m, image)
assert ctypes.c_void_p.in_dll(ctypes.pythonapi, "PyExc_SystemError").value == id(SystemError)
assert ctypes.pythonapi.Py_IsInitialized() == 1
`)
		})
		return
	}
	do(t, func() error {
		return py.Exec(`
import ctypes
g = ctypes.CDLL(None)
assert ctypes.c_void_p.in_dll(g, "PyExc_SystemError").value == id(SystemError)
assert ctypes.cast(g.PyLong_FromLong, ctypes.c_void_p).value
`)
	})
}

// evalObj evaluates expr in __main__ and returns the object.
func evalObj(t *testing.T, expr string) *py.Object {
	t.Helper()
	o, err := py.Eval(expr)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// A bytearray's buffer is shared memory: Go writes show in Python, the addresses match,
// and the bytearray cannot be resized until the buffer is closed.
func TestBufferBytearray(t *testing.T) {
	do(t, func() error {
		if err := py.Exec("import ctypes\nba = bytearray(b'hello')"); err != nil {
			return err
		}
		ba := evalObj(t, "ba")
		defer ba.Close()
		b, err := ba.Buffer(true)
		if err != nil {
			return err
		}
		bs, err := b.Bytes()
		if err != nil {
			return err
		}
		if string(bs) != "hello" || b.ReadOnly() || b.Format() != "B" || b.ItemSize() != 1 {
			t.Errorf("got %q readonly=%v format=%q itemsize=%d", bs, b.ReadOnly(), b.Format(), b.ItemSize())
		}
		addr := evalObj(t, "ctypes.addressof((ctypes.c_char * 5).from_buffer(ba))")
		if a, _ := addr.Int64(); uintptr(a) != uintptr(unsafe.Pointer(&bs[0])) {
			t.Errorf("Python's address %#x differs from Go's %p: copied", a, &bs[0])
		}
		addr.Close()
		bs[0] = 'j'
		if err := py.Exec("assert ba == b'jello', ba\n" +
			"try:\n    ba.append(33)\nexcept BufferError:\n    pass\nelse:\n    raise AssertionError('resized while exported')"); err != nil {
			return err
		}
		b.Close()
		b.Close()
		if _, err := b.Bytes(); err == nil {
			t.Error("Bytes after Close succeeded")
		}
		return py.Exec("ba.append(33)\nassert ba == b'jello!'")
	})
}

func TestBufferReadOnly(t *testing.T) {
	do(t, func() error {
		o := evalObj(t, "b'abc'")
		defer o.Close()
		if _, err := o.Buffer(true); err == nil {
			t.Error("a writable buffer of bytes was granted")
		} else if pe, ok := err.(*py.Error); !ok || pe.Type != "BufferError" {
			t.Errorf("got %v, want BufferError", err)
		}
		b, err := o.Buffer(false)
		if err != nil {
			return err
		}
		defer b.Close()
		bs, _ := b.Bytes()
		if !b.ReadOnly() || string(bs) != "abc" {
			t.Errorf("readonly=%v bytes=%q", b.ReadOnly(), bs)
		}
		if _, err := evalObj(t, "1").Buffer(false); err == nil {
			t.Error("an int exported a buffer")
		}
		return nil
	})
}

func TestBufferTyped(t *testing.T) {
	do(t, func() error {
		if err := py.Exec("import array\nad = array.array('d', [1.5, 2.5, 3.5])\nai = array.array('i', [7, -8])\n" +
			"m2 = memoryview(bytearray(48)).cast('d', (2, 3))"); err != nil {
			return err
		}
		ad := evalObj(t, "ad")
		defer ad.Close()
		b, err := ad.Buffer(true)
		if err != nil {
			return err
		}
		f, err := py.View[float64](b)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(f, []float64{1.5, 2.5, 3.5}) {
			t.Errorf("got %v", f)
		}
		f[2] = 42
		if _, err := py.View[int64](b); err == nil {
			t.Error("View[int64] of a 'd' buffer succeeded")
		}
		b.Close()
		if err := py.Exec("assert ad[2] == 42.0, ad"); err != nil {
			return err
		}

		ai := evalObj(t, "ai")
		defer ai.Close()
		bi, err := ai.Buffer(false)
		if err != nil {
			return err
		}
		defer bi.Close()
		if v, err := py.View[int32](bi); err != nil || !reflect.DeepEqual(v, []int32{7, -8}) {
			t.Errorf("View[int32] = %v, %v", v, err)
		}

		m2 := evalObj(t, "m2")
		defer m2.Close()
		b2, err := m2.Buffer(true)
		if err != nil {
			return err
		}
		defer b2.Close()
		v2, err := py.View[float64](b2)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(b2.Shape(), []int{2, 3}) || len(v2) != 6 || !b2.Contiguous() {
			t.Errorf("shape %v strides %v len %d", b2.Shape(), b2.Strides(), len(v2))
		}
		v2[4] = 9 // row 1, column 1
		return py.Exec("assert m2[1, 1] == 9.0")
	})
}

func TestBufferStrided(t *testing.T) {
	do(t, func() error {
		o := evalObj(t, "memoryview(bytearray(range(10)))[::2]")
		defer o.Close()
		b, err := o.Buffer(false)
		if err != nil {
			return err
		}
		defer b.Close()
		if b.Contiguous() || !reflect.DeepEqual(b.Shape(), []int{5}) || !reflect.DeepEqual(b.Strides(), []int{2}) {
			t.Errorf("contiguous=%v shape=%v strides=%v", b.Contiguous(), b.Shape(), b.Strides())
		}
		if _, err := b.Bytes(); err == nil {
			t.Error("Bytes of a strided buffer succeeded")
		}
		return nil
	})
}

// An unreachable, unclosed Buffer is released by a later entry into Python.
func TestBufferCleanup(t *testing.T) {
	do(t, func() error {
		if err := py.Exec("gc_ba = bytearray(4)"); err != nil {
			return err
		}
		o := evalObj(t, "gc_ba")
		defer o.Close()
		if _, err := o.Buffer(true); err != nil { // dropped unclosed
			return err
		}
		if py.Exec("gc_ba.append(0)") == nil {
			t.Error("resized while exported")
		}
		return nil
	})
	for range 100 {
		runtime.GC()
		// The release happens when a goroutine enters Python after the cleanup has run.
		if err := py.Do(func() error { return py.Exec("gc_ba.append(0)") }); err == nil {
			return
		}
	}
	t.Error("the buffer was never released")
}

// setMain binds name to v in __main__.
func setMain(t *testing.T, name string, v any) {
	t.Helper()
	m, err := py.Import("__main__")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.SetAttr(name, v); err != nil {
		t.Fatal(err)
	}
}

// NewBuffer shares a Go slice with Python: both sides read and write the same memory.
func TestNewBuffer(t *testing.T) {
	data := []float64{1, 2, 3}
	do(t, func() error {
		o, err := py.NewBuffer(data, false)
		if err != nil {
			return err
		}
		setMain(t, "gb", o)
		o.Close()
		if err := py.Exec("import ctypes\nmv = memoryview(gb)\nassert mv.format == 'd' and mv.tolist() == [1.0, 2.0, 3.0], mv.tolist()\nmv[0] = 5.0"); err != nil {
			return err
		}
		addr := evalObj(t, "ctypes.addressof(ctypes.c_double.from_buffer(gb))")
		defer addr.Close()
		if a, _ := addr.Int64(); uintptr(a) != uintptr(unsafe.Pointer(&data[0])) {
			t.Errorf("Python's address %#x differs from Go's %p", a, &data[0])
		}
		return nil
	})
	if data[0] != 5 {
		t.Errorf("Python's write is not visible in Go: %v", data)
	}
	data[2] = 9
	do(t, func() error { return py.Exec("assert mv[2] == 9.0, mv.tolist()\ndel mv, gb") })
}

func TestNewBufferLayouts(t *testing.T) {
	m := []int32{0, 1, 2, 3, 4, 5}
	ro := []uint8("abc")
	do(t, func() error {
		o, err := py.NewBuffer(m, false, 2, 3)
		if err != nil {
			return err
		}
		setMain(t, "m2", o)
		o.Close()
		r, err := py.NewBuffer(ro, true)
		if err != nil {
			return err
		}
		setMain(t, "ro", r)
		r.Close()
		for _, v := range []struct {
			o      func() (*py.Object, error)
			format string
		}{
			{func() (*py.Object, error) { return py.NewBuffer([]bool{true}, true) }, "?"},
			{func() (*py.Object, error) { return py.NewBuffer([]complex128{1i}, true) }, "Zd"},
			{func() (*py.Object, error) { return py.NewBuffer([]int{1}, true) }, "q"},
			{func() (*py.Object, error) { return py.NewBuffer([]uint16{1}, true) }, "H"},
			{func() (*py.Object, error) { return py.NewBuffer([]float32{}, true) }, "f"},
		} {
			o, err := v.o()
			if err != nil {
				return err
			}
			setMain(t, "x", o)
			o.Close()
			f := evalObj(t, "memoryview(x).format")
			if s, _ := f.GoString(); s != v.format {
				t.Errorf("format %q, want %q", s, v.format)
			}
			f.Close()
		}
		if _, err := py.NewBuffer(m, false, 4, 2); err == nil {
			t.Error("a shape of the wrong size was accepted")
		}
		return py.Exec(`
v = memoryview(m2)
assert v.shape == (2, 3) and v.strides == (12, 4) and v[1, 2] == 5, (v.shape, v.strides)
v[0, 1] = -1
r = memoryview(ro)
assert r.readonly and bytes(r) == b"abc"
try:
    r[0] = 0
except TypeError:
    pass
else:
    raise AssertionError("wrote a read-only Go buffer")
del v, r, m2, ro, x
`)
	})
	if m[1] != -1 {
		t.Errorf("got %v", m)
	}
}

// A Go buffer read back through Object.Buffer is the same memory, and Python freeing it
// unpins the slice.
func TestNewBufferRoundTripAndRelease(t *testing.T) {
	data := make([]float64, 4)
	before := py.ReadStats().GoBuffers
	do(t, func() error {
		o, err := py.NewBuffer(data, false)
		if err != nil {
			return err
		}
		defer o.Close()
		if n := py.ReadStats().GoBuffers; n != before+1 {
			t.Errorf("%d Go buffers pinned while exported, want %d", n, before+1)
		}
		b, err := o.Buffer(true)
		if err != nil {
			return err
		}
		defer b.Close()
		v, err := py.View[float64](b)
		if err != nil {
			return err
		}
		if &v[0] != &data[0] {
			t.Errorf("round trip changed the address: %p != %p", &v[0], &data[0])
		}
		return nil
	})
	if n := py.ReadStats().GoBuffers; n != before {
		t.Errorf("%d Go buffers pinned after release, want %d", n, before)
	}
}

func TestWithView(t *testing.T) {
	do(t, func() error {
		if err := py.Exec("import array\nwv = array.array('d', [1.0, 2.0, 3.0])"); err != nil {
			return err
		}
		o := evalObj(t, "wv")
		defer o.Close()
		var sum float64
		if err := py.WithView(o, true, func(v []float64) error {
			for i := range v {
				sum += v[i]
				v[i] *= 2
			}
			return nil
		}); err != nil {
			return err
		}
		if sum != 6 {
			t.Errorf("sum %v", sum)
		}
		// Released afterwards: the array can grow again.
		if err := py.Exec("assert wv.tolist() == [2.0, 4.0, 6.0], wv\nwv.append(8.0)"); err != nil {
			return err
		}
		// A type mismatch fails before fn runs; fn's error is returned.
		if err := py.WithView(o, false, func([]int32) error { t.Error("fn ran"); return nil }); err == nil {
			t.Error("WithView[int32] of a 'd' array succeeded")
		}
		errStop := errors.New("stop")
		if err := py.WithView(o, false, func([]float64) error { return errStop }); err != errStop {
			t.Errorf("got %v, want fn's error", err)
		}
		// A panic in fn still releases the buffer.
		func() {
			defer func() { recover() }()
			py.WithView(o, true, func([]float64) error { panic("boom") })
		}()
		if err := py.Exec("wv.append(10.0)"); err != nil {
			t.Errorf("not released after a panic: %v", err)
		}
		// Release inside fn: other goroutines run Python while the buffer is held.
		return py.WithView(o, false, func(v []float64) error {
			py.Release(func() {
				if err := py.Do(func() error { return py.Exec("x = sum(range(10))") }); err != nil {
					t.Error(err)
				}
			})
			if v[0] != 2 {
				t.Errorf("v[0] = %v after Release", v[0])
			}
			return nil
		})
	})
}

// A Buffer is reusable: BufferInto exports into a closed (or zero) Buffer, refuses an open
// one, and the export/close cycle allocates nothing in Go once warm.
func TestBufferInto(t *testing.T) {
	do(t, func() error {
		a := evalObj(t, "bytearray(b'abcd')")
		defer a.Close()
		if err := py.Exec("import array"); err != nil {
			return err
		}
		c := evalObj(t, "array.array('d', [1.0])")
		defer c.Close()
		var b py.Buffer
		for range 3 {
			if err := a.BufferInto(&b, true); err != nil {
				return err
			}
			if bs, _ := b.Bytes(); string(bs) != "abcd" {
				t.Errorf("got %q", bs)
			}
			if err := a.BufferInto(&b, true); err == nil {
				t.Error("BufferInto into an open Buffer succeeded")
			}
			b.Close()
			if err := c.BufferInto(&b, false); err != nil {
				return err
			}
			if v, err := py.View[float64](&b); err != nil || v[0] != 1 {
				t.Errorf("View = %v, %v", v, err)
			}
			b.Close()
		}
		if _, err := b.Bytes(); err == nil {
			t.Error("Bytes of a closed Buffer succeeded")
		}
		allocs := testing.AllocsPerRun(100, func() {
			if err := a.BufferInto(&b, true); err != nil {
				t.Fatal(err)
			}
			b.Close()
		})
		// Reuse must avoid the allocations of a fresh wrapper. The internal
		// native-view test checks it against the current C-call boundary cost.
		fresh := testing.AllocsPerRun(100, func() {
			nb, err := a.Buffer(true)
			if err != nil {
				t.Fatal(err)
			}
			nb.Close()
		})
		t.Logf("allocations per export+close: reused %v, fresh %v", allocs, fresh)
		if allocs >= fresh {
			t.Errorf("BufferInto+Close allocates %v times per run (a fresh Buffer: %v)", allocs, fresh)
		}
		return nil
	})
}
