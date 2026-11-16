package py_test

import (
	"fmt"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

// keptArg is the argument keepArg kept, wrongly, past its call: what a Func must not do.
var keptArg *py.Object

func keepArg(a []*py.Object, _ py.Kwargs) (any, error) {
	keptArg = a[0]
	return nil, nil
}

// probeKept, called during a later call, reports what the kept argument reads as: the
// int it aliases, or "panic".
func probeKept(a []*py.Object, _ py.Kwargs) (out any, _ error) {
	defer func() {
		if recover() != nil {
			out = "panic"
		}
	}()
	n, err := keptArg.Int64()
	if err != nil {
		return err.Error(), nil
	}
	return fmt.Sprint(n), nil
}

// Config.CheckBorrows reaches the calls: in a process started with it, an argument kept
// past its call panics during every later call, where in one started without it some
// later calls read their own argument through it (their memory is reused), which shows
// that the probe can see the difference. The children run with one P and no GC, so that
// the pool hands the kept call's scope to the next calls.
func TestConfigCheckBorrows(t *testing.T) {
	do(t, func() error {
		return py.Exec(`
import os, subprocess, sys
script = "import gocfg; gocfg.keep(1001); print(' '.join(gocfg.probe(i) for i in range(20)))"
def run(check):
    # One P and no GC: the calls after keep() then take the call scopes the pool holds,
    # deterministically, so that the unchecked child shows its reuse.
    env = dict(os.environ, PYTHAGO_TEST_CHECK_BORROWS="1" if check else "0", GOMAXPROCS="1", GOGC="off")
    r = subprocess.run([sys.executable, "-c", script], env=env, capture_output=True, text=True, timeout=60)
    assert r.returncode == 0, (r.returncode, r.stdout, r.stderr)
    return r.stdout.split()
checked, unchecked = run(True), run(False)
assert checked == ["panic"] * 20, "with CheckBorrows, the kept argument read %r" % checked
assert any(v != "panic" for v in unchecked), "without CheckBorrows no later call reused the kept argument's memory (%r): the probe cannot tell" % unchecked
`)
	})
}
