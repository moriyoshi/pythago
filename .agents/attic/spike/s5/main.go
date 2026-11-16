// Command s5 loads third-party C extensions (manylinux wheels) into the statically
// linked libpython through a runtime-generated SHN_ABS shim (.agents/attic/spike/elfshim).
//
//	PYTHONHOME=<pbs install> s5 <unpacked-wheels-dir> [code]
//
// S5_NOSHIM=1 skips the shim to show that extension imports then fail.
package main

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"time"

	"github.com/ebitengine/purego"

	"github.com/moriyoshi/pythago/internal/spike/elfshim"
	"github.com/moriyoshi/pythago/internal/spike/pyapi"
	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

func init() { runtime.LockOSThread() }

// Only the C API is exported. The binary also carries OpenSSL, sqlite, zlib, libffi
// and others; exporting those would let wheels' vendored copies bind to ours.
var exported = regexp.MustCompile(`^_?Py`)

const defaultCode = `
import sys, threading
import markupsafe, markupsafe._speedups
print("markupsafe", markupsafe.escape("<a href='x'>&</a>"), markupsafe._speedups.__file__.rsplit("/", 1)[-1])

import cffi
ffi = cffi.FFI()
ffi.cdef("int abs(int); void qsort(void *, size_t, size_t, int (*)(const void *, const void *));")
C = ffi.dlopen(None)
@ffi.callback("int(const void *, const void *)")
def cmp(a, b):
    x, y = ffi.cast("int *", a)[0], ffi.cast("int *", b)[0]
    return (x > y) - (x < y)
arr = ffi.new("int[]", [5, 3, 1, 4, 2])
C.qsort(arr, 5, ffi.sizeof("int"), cmp)
print("cffi", cffi.__version__, C.abs(-7), list(arr))

import numpy as np
a = np.arange(12, dtype=np.float64).reshape(3, 4)
m = np.random.default_rng(1).standard_normal((200, 200))
inv = np.linalg.inv(m)
print("numpy", np.__version__, a.sum(), np.allclose(m @ inv, np.eye(200)),
      np.round(np.abs(np.fft.fft([1, 0, -1, 0])), 3).tolist())
out = []
def work(i):
    out.append(float((np.ones((300, 300)) @ np.ones((300, 300)))[0, 0]) + i)
ts = [threading.Thread(target=work, args=(i,)) for i in range(8)]
[t.start() for t in ts]; [t.join() for t in ts]
print("numpy threads", sorted(out) == [300.0 + i for i in range(8)])

import ctypes
for name in ("ffi_call", "SSL_new", "sqlite3_open", "deflate"):
    try:
        getattr(ctypes.CDLL(None), name)
        print("LEAKED", name)
    except AttributeError:
        pass
print("non-Py symbols hidden")
`

func main() {
	var (
		initialize func(int32)
		run        func(string) int32
		finalize   func() int32
	)
	pyapi.Load(pyembed.SymbolAddr0())
	pyapi.Register(&initialize, "Py_InitializeEx")
	pyapi.Register(&run, "PyRun_SimpleString")
	pyapi.Register(&finalize, "Py_FinalizeEx")

	if os.Getenv("S5_NOSHIM") == "" {
		var syms []elfshim.Symbol
		for name, addr := range pyapi.Symbols {
			if exported.MatchString(name) {
				syms = append(syms, elfshim.Symbol{Name: name, Addr: addr})
			}
		}
		start := time.Now()
		h, err := elfshim.Load("libpython3.13.so.1.0", syms)
		if err != nil {
			fmt.Fprintln(os.Stderr, "shim:", err)
			os.Exit(1)
		}
		for _, name := range []string{"PyLong_Type", "_Py_NoneStruct", "PyExc_ValueError", "PyObject_GetAttr"} {
			got, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
			if err != nil || got != pyapi.Addr(name) {
				fmt.Fprintf(os.Stderr, "dlsym %s = %#x, %v; want %#x\n", name, got, err, pyapi.Addr(name))
				os.Exit(1)
			}
		}
		fmt.Printf("shim: %d symbols, handle %#x, %v\n", len(syms), h, time.Since(start))
	}

	initialize(0)
	code := defaultCode
	if len(os.Args) > 2 {
		code = os.Args[2]
	}
	rc := run(fmt.Sprintf("import sys; sys.path.insert(0, %q)\n", os.Args[1]) + code)
	if finalize() != 0 {
		fmt.Fprintln(os.Stderr, "Py_FinalizeEx failed")
	}
	os.Exit(int(rc))
}
