// Package testwheels gives tests a site directory of real binary wheels: the wheels in
// the directory named by PYTHAGO_TEST_WHEELS (internal/tools/fetchwheels writes it, for
// one target; the CI scripts set it), extracted into a new temporary directory that tests
// put on sys.path (py.Config.SearchPaths) before py.Init.
//
// Without the variable there is no site directory, and tests that need one skip; with it,
// a missing or broken package is a test failure, not a skip.
//
// Wheels are untrusted data: an entry naming an absolute path, a path outside the
// directory or a symbolic link is an error. Each file keeps its mode bits (an extension's
// $ORIGIN-relative library directory such as pyzmq.libs keeps its layout).
package testwheels

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Env names the directory of wheels to extract.
const Env = "PYTHAGO_TEST_WHEELS"

// siteEnv passes an extracted site directory to processes the test starts (a test binary
// started as sys.executable runs the same configuration), which reuse it.
const siteEnv = "PYTHAGO_TEST_WHEELS_SITE"

var (
	once    sync.Once
	site    string
	wheels  []string
	siteErr error
	owned   bool
)

// Site returns the site directory, extracting the wheels the first time it is called:
// "" and no error when PYTHAGO_TEST_WHEELS is unset. It needs no *testing.T, so that
// TestMain can call it before py.Init.
func Site() (string, error) {
	once.Do(func() { site, wheels, siteErr = extractAll() })
	return site, siteErr
}

// Wheels lists the extracted wheels' file names (after Site).
func Wheels() []string { return wheels }

// Remove deletes the site directory if this process extracted it. Call it after
// py.Finalize; loaded libraries can keep files from being removed on Windows, which is
// not an error here.
func Remove() {
	if owned && site != "" {
		os.RemoveAll(site)
	}
}

func extractAll() (string, []string, error) {
	src := os.Getenv(Env)
	if src == "" {
		return "", nil, nil
	}
	src, err := filepath.Abs(src)
	if err != nil {
		return "", nil, err
	}
	names, err := filepath.Glob(filepath.Join(src, "*.whl"))
	if err != nil {
		return "", nil, err
	}
	if len(names) == 0 {
		return "", nil, fmt.Errorf("%s=%s: no wheels there", Env, src)
	}
	sort.Strings(names)
	var files []string
	for _, n := range names {
		files = append(files, filepath.Base(n))
	}
	if dir := os.Getenv(siteEnv); dir != "" {
		if _, err := os.Stat(dir); err == nil {
			return dir, files, nil
		}
	}
	dir, err := os.MkdirTemp("", "pythago-wheels-")
	if err != nil {
		return "", nil, err
	}
	for _, n := range names {
		if err := extract(n, dir); err != nil {
			os.RemoveAll(dir)
			return "", nil, fmt.Errorf("%s: %w", filepath.Base(n), err)
		}
	}
	owned = true
	os.Setenv(siteEnv, dir)
	return dir, files, nil
}

// extract unpacks one wheel into dir.
func extract(wheel, dir string) error {
	zr, err := zip.OpenReader(wheel)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("entry %q leaves the directory", f.Name)
		}
		dst := filepath.Join(dir, name)
		mode := f.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			return fmt.Errorf("entry %q is a symbolic link", f.Name)
		case mode.IsDir() || strings.HasSuffix(f.Name, "/"):
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("entry %q is not a regular file (%v)", f.Name, mode)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		perm := mode.Perm()
		if perm == 0 { // a zip without Unix modes
			perm = 0o644
		}
		if err := writeFile(f, dst, perm|0o200); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(f *zip.File, dst string, perm fs.FileMode) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return errors.Join(err, w.Close())
}

// ZmqUnsupported returns why libzmq cannot run here, or "": under Wine on arm64, libzmq's
// clock reads the cycle counter (mrs pmccntr_el0), which Windows lets user code do and
// Linux does not, so Wine stops the process with an illegal instruction (stock CPython
// too) at the first socket.
func ZmqUnsupported(goos, goarch string) string {
	if goos == "windows" && goarch == "arm64" && UnderWine() {
		return "libzmq reads pmccntr_el0, an illegal instruction under Wine on arm64 (stock CPython too)"
	}
	return ""
}
