package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/moriyoshi/bao/build"
	"github.com/moriyoshi/pythago/glue"
	"github.com/moriyoshi/pythago/internal/pbs"
)

// runShared generates the dist package from the PBS install_only build: bao translates
// its libpython shared object (-linux-shared-object), with the glue as the only
// relocatable input and libpython's pthread_create, _join, _detach and _exit imports
// wrapped to it. bao needs no linker for a single-object input, and with a prebuilt glue
// object (package glue) nothing is compiled, so no LLVM is involved.
func runShared(o *options) error {
	flavor := "install_only_stripped"
	pyver := o.python
	if o.variant == "freethreaded" {
		flavor, pyver = "freethreaded-"+flavor, pyver+"t"
	}
	rel := pbs.Release{Tag: o.release, Cache: o.cache, Log: log.Printf}
	a, err := rel.Find(o.python, o.triple, flavor)
	if err != nil {
		return err
	}
	dir, err := rel.Extract(a)
	if err != nil {
		return err
	}
	py := filepath.Join(dir, "python")
	so := filepath.Join(py, "lib", "libpython"+pyver+".so.1.0")
	if o.goos == "darwin" {
		so = filepath.Join(py, "lib", "libpython"+pyver+".dylib")
	}
	if _, err := os.Stat(so); err != nil {
		return fmt.Errorf("no %s in %s", filepath.Base(so), a.Name)
	}
	meta := pbsMeta{version: o.python, target: o.triple}
	if o.goos == "linux" {
		// PBS links against glibc 2.17, which the library's imports keep (bao binds the
		// versions in its verneed); the cap covers the glue's imports and bao's runtime.
		meta.glibcFloor = minGlibc
	}
	if o.variant == "freethreaded" {
		meta.abiTag = "t"
	}
	log.Printf("PBS: Python %s (%s), %s%s", pyver, o.triple, filepath.Base(so), meta.floorNote())

	glueObj, err := sharedGlue(o, filepath.Join(py, "include", "python"+pyver), meta)
	if err != nil {
		return err
	}
	bo := build.Options{Input: glueObj, Wrap: wrappedSymbols}
	if o.goos == "darwin" {
		bo.Darwin.Dylib = so
	} else {
		bo.Linux.SharedObject = so
	}
	if err := baoBuild(o, meta, bo); err != nil {
		return err
	}
	targetPython := filepath.Join(py, "bin", "python"+pyver)
	if o.goos != runtime.GOOS || o.goarch != runtime.GOARCH {
		targetPython = "" // packStdlib falls back to this host's stock build
	}
	if err := packStdlib(o, filepath.Join(py, "lib", "python"+pyver), targetPython, meta); err != nil {
		return err
	}
	return emit(o, filepath.Join(py, "share", "licenses"), meta, true)
}

// sharedGlue returns the glue as one relocatable object (glue sources plus a
// pythago_symtab of their pythago_* functions): the prebuilt object for this target if
// package glue has one, else compiled with LLVM.
func sharedGlue(o *options, include string, meta pbsMeta) (string, error) {
	key := glue.Key(meta.pyver(), o.goos, o.goarch)
	if err := os.MkdirAll(o.work, 0o755); err != nil {
		return "", err
	}
	if data, ok := glue.Prebuilt(key); ok {
		log.Printf("glue: prebuilt %s", key)
		path := filepath.Join(o.work, "glue-"+key+".o")
		return path, os.WriteFile(path, data, 0o644)
	}
	log.Printf("glue: no prebuilt object for %s; compiling", key)
	if o.goos != "darwin" { // compileDarwinGlue picks its clang
		if err := o.needLLVM(); err != nil {
			return "", err
		}
	}
	return compileSharedGlue(o, include)
}
