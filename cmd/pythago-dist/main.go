// Command pythago-dist generates a dist package: a CPython native image for one
// (Python version, variant, target), translated by bao from a python-build-standalone
// (PBS) "full" archive, plus the standard library as an embedded zip. Importing the
// package registers both with package py.
//
//	pythago-dist -python 3.13 -target linux/arm64 -o <dir>
//
// The toolchain (LLVM, bao) defaults to PYTHAGO_LLVM / PYTHAGO_BAO, else verified versions
// fetched into the user cache on first use, so `go generate` needs only Go:
//
//	//go:generate env -u GOOS -u GOARCH go run github.com/moriyoshi/pythago/cmd/pythago-dist -python 3.13 -target $GOOS/$GOARCH -o pydist
//
// (env -u keeps a cross GOARCH from making `go run` build this generator for the target.)
//
// Steps: fetch and verify the PBS archive; LTO-lower its bitcode libpython with a
// regenerated inittab; add the glue and the symbol table; run bao (imports bound at the
// archive's glibc floor); package the stdlib (with .pyc files when a Python of the same
// version can run on this host); write the registration file. Needs bao with
// -linux-max-symbol-version (bao main after PR #18).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/moriyoshi/pythago/internal/pbs"
	"github.com/moriyoshi/pythago/internal/toolchain"
)

// resolveToolchain fills o.llvm and o.bao (see package toolchain).
func resolveToolchain(o *options, cacheRoot string) error {
	var err error
	if o.llvm, err = toolchain.LLVM(o.llvm, cacheRoot); err != nil {
		return err
	}
	o.bao = toolchain.Bao(o.bao)
	return nil
}

type options struct {
	python, variant, target, release string
	pbs, llvm, bao, sysroot, sdk     string
	work, cache, out, pkg, pycPython string
	skipLTO, staticTLS               bool
	goos, goarch, triple             string
	mode, cacheRoot, windowsSDK      string
	llvmResolved                     bool
}

func main() {
	var o options
	flag.StringVar(&o.python, "python", "3.13", "Python version (major.minor)")
	flag.StringVar(&o.variant, "variant", "gil", "gil or freethreaded")
	flag.StringVar(&o.target, "target", runtime.GOOS+"/"+runtime.GOARCH, "goos/goarch")
	flag.StringVar(&o.release, "release", pbs.DefaultRelease, "python-build-standalone release tag")
	flag.StringVar(&o.pbs, "pbs", "", "use this extracted PBS directory (containing PYTHON.json) instead of fetching")
	flag.StringVar(&o.llvm, "llvm", "", "LLVM bin directory (clang, ld.lld, llvm-ar, llvm-nm), at least the PBS clang version (default: $PYTHAGO_LLVM, else fetch LLVM "+toolchain.LLVMVersion+")")
	flag.StringVar(&o.bao, "bao", "", "bao executable to run instead of the bao built into pythago-dist (default: $PYTHAGO_BAO)")
	prebuild := flag.String("prebuild-glue", "", "maintainers: build the shared-object glue objects for every supported target into this directory (glue/prebuilt), and exit")
	printToolchain := flag.Bool("print-toolchain", false, "resolve (and fetch) the toolchain, print llvm=DIR and bao=PATH (empty: built in), and exit")
	flag.StringVar(&o.sysroot, "sysroot", "", "glibc sysroot for a linux target (default / for native builds)")
	flag.StringVar(&o.sdk, "sdk", "", "macOS SDK directory for a darwin target (default: xcrun's, on macOS)")
	flag.StringVar(&o.windowsSDK, "windows-sdk", os.Getenv("PYTHAGO_WINDOWS_SDK"), "directory with Wine's msvcrt/ and windows/ headers and lib/<goarch>/ import libraries (ci/windows-wine.sh sdk), to compile the glue for a windows target without a prebuilt one (default: $PYTHAGO_WINDOWS_SDK)")
	flag.StringVar(&o.work, "work", "", "work directory (default: under the user cache directory)")
	flag.StringVar(&o.cache, "cache", "", "download cache (default: under the user cache directory)")
	flag.StringVar(&o.out, "o", "", "output package directory")
	flag.StringVar(&o.pkg, "package", "", "Go package name (default: the output directory's base name)")
	flag.StringVar(&o.pycPython, "pyc-python", "", "python of the same version to compile the stdlib's .pyc files (default: the PBS python if it runs here; none: skip)")
	flag.StringVar(&o.mode, "mode", "shared", "shared: translate the PBS install_only libpython shared object (no LLVM with a prebuilt glue object); lto: LTO-lower the PBS full archive's bitcode libpython (needs LLVM)")
	flag.BoolVar(&o.staticTLS, "static-tls", false, "experimental: put the image's TLS in glibc static TLS at startup when glibc allows it (bao -linux-static-tls); the package's TLSStatic() reports whether it did")
	flag.BoolVar(&o.skipLTO, "skip-lto", false, "reuse the LTO-lowered libpython.o in the work directory even if its inputs changed")
	flag.Parse()
	if *printToolchain {
		cacheRoot, err := os.UserCacheDir()
		if err == nil {
			err = resolveToolchain(&o, cacheRoot)
		}
		if err != nil {
			log.Fatal("pythago-dist: ", err)
		}
		fmt.Printf("llvm=%s\nbao=%s\n", o.llvm, o.bao)
		return
	}
	if *prebuild != "" {
		o.out = *prebuild // satisfies run's checks; prebuildGlue writes only to the directory
		o.mode = "prebuild"
	}
	if err := run(&o); err != nil {
		log.Fatal("pythago-dist: ", err)
	}
}

func run(o *options) error {
	if o.out == "" {
		return fmt.Errorf("-o is required")
	}
	var ok bool
	if o.goos, o.goarch, ok = strings.Cut(o.target, "/"); !ok {
		return fmt.Errorf("unsupported target %q", o.target)
	}
	var err error
	if o.triple, err = pbs.Triple(o.goos, o.goarch); err != nil {
		return fmt.Errorf("unsupported target %q (linux, darwin or windows, arm64 or amd64)", o.target)
	}
	if o.variant != "gil" && o.variant != "freethreaded" {
		return fmt.Errorf("-variant must be gil or freethreaded")
	}
	switch o.goos {
	case "linux":
		if (o.goarch != runtime.GOARCH || runtime.GOOS != "linux") && o.sysroot == "" {
			return fmt.Errorf("cross builds need -sysroot")
		}
	case "windows":
		if o.mode != "shared" {
			return fmt.Errorf("windows targets support only -mode shared")
		}
	case "darwin":
		if o.mode != "shared" && o.mode != "prebuild" {
			return fmt.Errorf("darwin targets support only -mode shared")
		}
		if runtime.GOOS != "darwin" && o.sdk == "" {
			return fmt.Errorf("darwin targets built on %s need -sdk (a macOS SDK directory)", runtime.GOOS)
		}
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	o.cacheRoot = cacheRoot
	if o.mode != "lto" && o.mode != "shared" && o.mode != "prebuild" {
		return fmt.Errorf("-mode must be lto or shared")
	}
	o.bao = toolchain.Bao(o.bao)
	key := fmt.Sprintf("%s-%s-%s-%s", o.python, o.variant, o.goarch, o.release)
	if o.mode == "shared" {
		key += "-shared"
	}
	if o.goos == "windows" {
		key += "-windows"
	}
	if o.cache == "" {
		o.cache = filepath.Join(cacheRoot, "pythago-dist", "downloads")
	}
	if o.work == "" {
		o.work = filepath.Join(cacheRoot, "pythago-dist", "work", key)
	}
	if o.pkg == "" {
		o.pkg = goIdent(filepath.Base(o.out))
	}
	for _, p := range []*string{&o.out, &o.work, &o.cache} {
		if *p, err = filepath.Abs(*p); err != nil {
			return err
		}
	}
	switch o.mode {
	case "shared":
		if o.goos == "windows" {
			return runWindows(o)
		}
		return runShared(o)
	case "prebuild":
		return prebuildGlue(o, o.out)
	}
	if err := o.needLLVM(); err != nil {
		return err
	}

	pbs := o.pbs
	if pbs == "" {
		if pbs, err = fetch(o); err != nil {
			return err
		}
	}
	meta, err := readMeta(pbs)
	if err != nil {
		return err
	}
	log.Printf("PBS: Python %s%s (%s)%s", meta.version, meta.abiTag, meta.target, meta.floorNote())
	archive, err := buildArchive(o, pbs, meta)
	if err != nil {
		return err
	}
	if err := runBao(o, pbs, meta, archive); err != nil {
		return err
	}
	if err := packStdlib(o, filepath.Join(pbs, "install", "lib", "python"+meta.pyver()), filepath.Join(pbs, "install", "bin", "python3"), meta); err != nil {
		return err
	}
	return emit(o, filepath.Join(pbs, "licenses"), meta, false)
}

// needLLVM resolves LLVM (see package toolchain) when something must be compiled.
func (o *options) needLLVM() error {
	if o.llvmResolved {
		return nil
	}
	dir, err := toolchain.LLVM(o.llvm, o.cacheRoot)
	if err != nil {
		return err
	}
	if o.llvm, err = filepath.Abs(dir); err != nil {
		return err
	}
	o.llvmResolved = true
	return nil
}

// goIdent turns a directory name into a Go package name.
func goIdent(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9' && b.Len() > 0, r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "pythondist"
	}
	return b.String()
}
