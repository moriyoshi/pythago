// Command s1 checks that a bao-translated libpython initializes and runs code.
// Run with PYTHONHOME pointing at an extracted python-build-standalone install tree.
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/moriyoshi/pythago/internal/spike/pyapi"
	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

func init() {
	// Python's thread state lives in native TLS; keep the main goroutine on one OS thread.
	runtime.LockOSThread()
}

func main() {
	var (
		initialize func()
		run        func(string) int32
		finalize   func() int32
	)
	pyapi.Load(pyembed.SymbolAddr0())
	pyapi.Register(&initialize, "Py_Initialize")
	pyapi.Register(&run, "PyRun_SimpleString")
	pyapi.Register(&finalize, "Py_FinalizeEx")

	code := `
import sys, json, sqlite3, ssl, decimal, ctypes, hashlib, zlib, threading
print("python", sys.version)
print("json", json.dumps({"ok": [1, 2.5, None]}))
db = sqlite3.connect(":memory:")
print("sqlite", db.execute("select sqlite_version()").fetchone()[0])
print("ssl", ssl.OPENSSL_VERSION)
print("decimal", decimal.Decimal(1) / decimal.Decimal(7))
print("sha256", hashlib.sha256(b"pythago").hexdigest()[:16])
out = []
t = threading.Thread(target=lambda: out.append(sum(range(10**6))))
t.start(); t.join()
print("thread", out)
`
	if len(os.Args) > 1 {
		code = os.Args[1]
	}
	initialize()
	rc := run(code)
	if finalize() != 0 {
		fmt.Fprintln(os.Stderr, "Py_FinalizeEx failed")
	}
	os.Exit(int(rc))
}
