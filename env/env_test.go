package env_test

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/moriyoshi/pythago/env"
)

// options picks the stock interpreter: PYTHAGO_TEST_PBS_PYTHON, or a fetched one with
// PYTHAGO_TEST_FETCH=1 (network).
func options(t *testing.T) env.Options {
	o := env.Options{Python: "3.13", Interpreter: os.Getenv("PYTHAGO_TEST_PBS_PYTHON")}
	if o.Interpreter == "" && os.Getenv("PYTHAGO_TEST_FETCH") == "" {
		t.Skip("set PYTHAGO_TEST_PBS_PYTHON to a stock PBS python3.13, or PYTHAGO_TEST_FETCH=1")
	}
	return o
}

// demoWheel writes a minimal pure-Python wheel with a console script.
func demoWheel(t *testing.T, dir string) string {
	path := filepath.Join(dir, "demo_pkg-1.0-py3-none-any.whl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"demo_pkg/__init__.py":                    "def main():\n    print('demo tool')\n",
		"demo_pkg-1.0.dist-info/METADATA":         "Metadata-Version: 2.1\nName: demo-pkg\nVersion: 1.0\n",
		"demo_pkg-1.0.dist-info/WHEEL":            "Wheel-Version: 1.0\nGenerator: pythago-test\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
		"demo_pkg-1.0.dist-info/entry_points.txt": "[console_scripts]\ndemo-tool = demo_pkg:main\n",
		"demo_pkg-1.0.dist-info/RECORD":           "",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return path
}

func TestCreateAndPip(t *testing.T) {
	o := options(t)
	dir := filepath.Join(t.TempDir(), "venv")
	if err := env.Create(dir, o); err != nil {
		t.Fatal(err)
	}
	if err := env.Create(dir, o); err != nil { // existing: checked and reused
		t.Fatal(err)
	}
	if err := env.Pip(dir, o, "install", "--no-index", "-q", demoWheel(t, t.TempDir())); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"lib/python3.13/site-packages/demo_pkg/__init__.py", "bin/demo-tool"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if err := env.Check(dir, env.Options{Python: "3.14"}); err == nil {
		t.Error("Check accepted a 3.13 environment for 3.14")
	}
	if err := env.Check(dir, env.Options{Python: "3.13", FreeThreaded: true}); err == nil {
		t.Error("Check accepted a GIL environment as free-threaded")
	}
}

func TestCheckMissing(t *testing.T) {
	err := env.Check(t.TempDir(), env.Options{Python: "3.13"})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("got %v, want os.ErrNotExist", err)
	}
}
