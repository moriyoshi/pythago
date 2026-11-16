// Package env provisions Python environments for programs that embed Python with package
// py: venv-like directories (bin/, lib/pythonX.Y/site-packages) for [py.Config.Environment].
//
// The provisioner is a stock python-build-standalone (PBS) Python of the same release,
// version and variant as the native image: it has the same ABI and platform tags, so
// `python -m venv` and pip create, resolve and install exactly what the image can load.
// The stock interpreter is fetched (verified against the release digest) into a cache
// unless Options.Interpreter names one. It must run on the host, so provision on (or for)
// the machine that runs the program.
package env

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/moriyoshi/pythago/internal/pbs"
)

// Options select the Python an environment is for.
type Options struct {
	// Python is the version, "3.13" or "3.14"; it must match the native image.
	Python string
	// FreeThreaded selects the free-threaded build (cp313t, cp314t).
	FreeThreaded bool
	// Release is the PBS release tag; it defaults to pbs.DefaultRelease and should match
	// the image's.
	Release string
	// Interpreter is a stock python3 to provision with instead of fetching one.
	Interpreter string
	// Cache holds fetched interpreters; it defaults to a directory under
	// os.UserCacheDir.
	Cache string
	// Stdout and Stderr receive venv's and pip's output; they default to os.Stderr.
	Stdout, Stderr io.Writer
}

func (o Options) out() io.Writer {
	if o.Stdout != nil {
		return o.Stdout
	}
	return os.Stderr
}

func (o Options) errOut() io.Writer {
	if o.Stderr != nil {
		return o.Stderr
	}
	return os.Stderr
}

// libDir is "python3.13", or "python3.14t" for a free-threaded build.
func (o Options) libDir() string {
	if o.FreeThreaded {
		return "python" + o.Python + "t"
	}
	return "python" + o.Python
}

// Interpreter returns the stock PBS python3 for o, fetching and extracting the
// install_only_stripped archive for this host's architecture on first use.
func Interpreter(o Options) (string, error) {
	if o.Interpreter != "" {
		return o.Interpreter, nil
	}
	if o.Python == "" {
		return "", errors.New("env: Options.Python is required")
	}
	triple, err := pbs.Triple(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	cache := o.Cache
	if cache == "" {
		c, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		cache = filepath.Join(c, "pythago", "pbs")
	}
	rel := pbs.Release{Tag: o.Release, Cache: cache, Log: log.Printf}
	if rel.Tag == "" {
		rel.Tag = pbs.DefaultRelease
	}
	flavor := "install_only_stripped"
	if o.FreeThreaded {
		flavor = "freethreaded-" + flavor
	}
	a, err := rel.Find(o.Python, triple, flavor)
	if err != nil {
		return "", err
	}
	dir, err := rel.Extract(a)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "python", "bin")
	for _, name := range []string{"python" + o.Python + map[bool]string{true: "t"}[o.FreeThreaded], "python3"} {
		if _, err := os.Stat(filepath.Join(bin, name)); err == nil {
			return filepath.Join(bin, name), nil
		}
	}
	return "", fmt.Errorf("env: no python3 in %s", bin)
}

// Create makes a venv at dir with the stock Python for o, or checks that an existing one
// was made for the same version and variant. pip comes from the interpreter's ensurepip.
func Create(dir string, o Options) error {
	if err := Check(dir, o); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	python, err := Interpreter(o)
	if err != nil {
		return err
	}
	cmd := exec.Command(python, "-I", "-m", "venv", dir)
	cmd.Stdout, cmd.Stderr = o.out(), o.errOut()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("env: %s -m venv %s: %w", python, dir, err)
	}
	return Check(dir, o)
}

// Pip runs pip in the environment at dir: Pip(dir, o, "install", "torch").
func Pip(dir string, o Options, args ...string) error {
	if err := Check(dir, o); err != nil {
		return err
	}
	python := filepath.Join(dir, "bin", "python")
	cmd := exec.Command(python, append([]string{"-I", "-m", "pip"}, args...)...)
	cmd.Stdout, cmd.Stderr = o.out(), o.errOut()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("env: pip %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// Check verifies that dir is an environment for o's version and variant: its pyvenv.cfg
// version, and its lib/pythonX.Y[t]/site-packages. A missing environment wraps
// os.ErrNotExist.
func Check(dir string, o Options) error {
	version, err := venvVersion(dir)
	if err != nil {
		return err
	}
	if version != o.Python && !strings.HasPrefix(version, o.Python+".") {
		return fmt.Errorf("env: %s is for Python %s, not %s", dir, version, o.Python)
	}
	sp := filepath.Join(dir, "lib", o.libDir(), "site-packages")
	if st, err := os.Stat(sp); err != nil || !st.IsDir() {
		return fmt.Errorf("env: %s has no %s (wrong variant?)", dir, filepath.Join("lib", o.libDir(), "site-packages"))
	}
	return nil
}

// venvVersion reads "version" (or "version_info") from dir/pyvenv.cfg.
func venvVersion(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "pyvenv.cfg"))
	if err != nil {
		return "", fmt.Errorf("env: %s is not an environment: %w", dir, err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		k, v, ok := strings.Cut(s.Text(), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "version", "version_info":
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("env: %s/pyvenv.cfg has no version", dir)
}
