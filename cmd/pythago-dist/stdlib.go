package main

import (
	"archive/zip"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/moriyoshi/pythago/internal/pbs"
)

// hostPython fetches this host's stock PBS Python (install_only_stripped) of o's version
// and variant.
func hostPython(o *options) (string, error) {
	triple, err := pbs.Triple(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	flavor := "install_only_stripped"
	if o.variant == "freethreaded" {
		flavor = "freethreaded-" + flavor
	}
	rel := pbs.Release{Tag: o.release, Cache: o.cache, Log: log.Printf}
	a, err := rel.Find(o.python, triple, flavor)
	if err != nil {
		return "", err
	}
	dir, err := rel.Extract(a)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "python", "bin", "python3"), nil
}

// Top-level stdlib entries left out of the embedded tree: tests, GUI and IDE code, the
// bundled pip, build configuration (which holds libpython.a), and directories of shared
// objects or third-party packages.
var stdlibDrop = []string{"test", "idlelib", "tkinter", "turtledemo", "ensurepip", "lib-dynload",
	"site-packages", "__pycache__", "lib2to3", "turtle.py"}

// packStdlib writes the standard library to stdlib_<goos>_<goarch>.zip in the output
// package: sources, plus unchecked-hash .pyc files under __pycache__ when a Python of the
// same version can run here (bytecode does not depend on the architecture).
func packStdlib(o *options, lib, targetPython string, meta pbsMeta) error {
	stage := filepath.Join(o.work, "stdlib")
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	err := filepath.WalkDir(lib, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(lib, p)
		if rel == "." {
			return nil
		}
		top := strings.Split(rel, string(filepath.Separator))[0]
		if slices.Contains(stdlibDrop, top) || strings.HasPrefix(top, "config-") ||
			d.Name() == "__pycache__" || d.Name() == "tests" || d.Name() == "idle_test" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(stage, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if strings.HasSuffix(p, ".so") {
			return nil
		}
		return copyFile(p, dst)
	})
	if err != nil {
		return err
	}

	python := o.pycPython
	if python == "" {
		python = targetPython
		if o.goarch != runtime.GOARCH || o.goos != runtime.GOOS {
			// The target's Python cannot run here; bytecode does not depend on the
			// architecture or OS, so use this host's stock build of the same version and
			// variant.
			host, err := hostPython(o)
			if err != nil {
				log.Printf("no host Python for .pyc files: %v", err)
			}
			python = host
		}
	}
	if python != "none" && python != "" {
		if out, err := exec.Command(python, "-I", "-c", "import sys; print('%d.%d' % sys.version_info[:2])").Output(); err != nil || strings.TrimSpace(string(out)) != meta.version {
			log.Printf("no Python %s runs here (%s); the stdlib has no .pyc files and compiles on import", meta.version, python)
		} else {
			log.Printf("compile .pyc files with %s", python)
			cmd := exec.Command(python, "-I", "-m", "compileall", "-q", "-j0", "--invalidation-mode", "unchecked-hash", stage)
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard // a few files are syntax-error fixtures
			cmd.Run()
		}
	}

	zipPath := filepath.Join(o.out, "stdlib_"+o.goos+"_"+o.goarch+".zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	files := 0
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(stage, p)
		// Stored, not deflated: py serves stored entries in place from the embedded bytes.
		w, err := zw.CreateHeader(&zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Store})
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		files++
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	st, _ := os.Stat(zipPath)
	log.Printf("stdlib: %d files, %d MB", files, st.Size()>>20)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
