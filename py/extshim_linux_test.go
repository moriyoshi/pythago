package py_test

import (
	"testing"

	"github.com/moriyoshi/pythago/py"
)

// linkMapNames sets names to the names of the loaded objects, from dl_iterate_phdr, which
// reports the link map's l_name, the field gdb reads.
const linkMapNames = `
import ctypes

class Info(ctypes.Structure):
    _fields_ = [("addr", ctypes.c_void_p), ("name", ctypes.c_char_p)]

names = []
Callback = ctypes.CFUNCTYPE(ctypes.c_int, ctypes.POINTER(Info), ctypes.c_size_t, ctypes.c_void_p)
def collect(info, size, data):
    names.append((info.contents.name or b"").decode())
    return 0
libc = ctypes.CDLL(None)
libc.dl_iterate_phdr.argtypes = [Callback, ctypes.c_void_p]
libc.dl_iterate_phdr(Callback(collect), None)
`

// By default the extension shim must not appear in the link map as /proc/self/fd/N: gdb
// opens every loaded object by its name in its own process, where N is often one of its
// pipes, and blocks reading it forever with the program stopped.
func TestExtensionShimLinkMapName(t *testing.T) {
	do(t, func() error {
		return py.Exec(linkMapNames + `
proc = [n for n in names if n.startswith("/proc/self/")]
assert not proc, f"objects loaded under /proc/self paths: {proc}; all: {names}"
shim = [n for n in names if n.endswith("/libpython-pythago.so")]
assert len(shim) == 1, f"want one libpython-pythago.so in the link map, got {names}"
`)
	})
}

// Config.ExtensionShimDir and Config.ExtensionShimMemfd choose where the shim is loaded
// from. Each case runs in a process started from sys.executable with testConfig's
// environment variables; the child prints the shim's link-map name after checking that
// the C API resolves from the global scope, which only the shim provides.
func TestExtensionShimSource(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import os, re, subprocess, sys, tempfile
probe = ` + "'''" + linkMapNames + `
assert hasattr(libc, "PyLong_FromLong"), "the C API is not in the global scope"
shim = [n for n in names if n.endswith("libpython-pythago.so") or n.startswith("/proc/self/fd/")]
assert len(shim) == 1, names
print(shim[0])
` + "'''" + `
def child(**env):
    e = {k: v for k, v in os.environ.items() if not k.startswith("PYTHAGO_TEST_EXTSHIM_")}
    e.update(env)
    return subprocess.run([sys.executable, "-c", probe], capture_output=True, text=True,
                          timeout=60, env=e)

with tempfile.TemporaryDirectory() as d:
    r = child(PYTHAGO_TEST_EXTSHIM_DIR=d)
    assert r.returncode == 0, r.stderr
    name = r.stdout.strip()
    assert re.fullmatch(re.escape(d) + r"/pythago-shim-[^/]+/libpython-pythago\.so", name), name
    assert not os.path.exists(os.path.dirname(name)), f"{name}'s directory was left behind"
    assert os.listdir(d) == [], os.listdir(d)

r = child(PYTHAGO_TEST_EXTSHIM_MEMFD="1")
assert r.returncode == 0, r.stderr
assert re.fullmatch(r"/proc/self/fd/\d+", r.stdout.strip()), r.stdout

# A directory that was asked for and cannot be used is an error, not a fallback to a memfd.
with tempfile.TemporaryDirectory() as d:
    r = child(PYTHAGO_TEST_EXTSHIM_DIR=os.path.join(d, "missing"))
assert r.returncode != 0 and "extension shim" in r.stderr and "missing" in r.stderr, (r.returncode, r.stdout, r.stderr)

with tempfile.TemporaryDirectory() as d:
    r = child(PYTHAGO_TEST_EXTSHIM_DIR=d, PYTHAGO_TEST_EXTSHIM_MEMFD="1")
assert r.returncode != 0 and "both a directory and a memfd" in r.stderr, (r.returncode, r.stdout, r.stderr)
`)
	})
}
