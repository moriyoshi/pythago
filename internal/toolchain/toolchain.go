// Package toolchain resolves the producer toolchain pythago-dist (and pythago's
// maintainer tools) use. bao is a Go module dependency that runs in process (an executable
// given explicitly or through PYTHAGO_BAO replaces it); LLVM, needed only for the LTO build
// and for compiling the glue, is given explicitly, through PYTHAGO_LLVM, or fetched at the
// verified version into the user cache.
package toolchain

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/moriyoshi/pythago/internal/ghrelease"
)

// LLVMVersion is the LLVM release fetched when none is given. It must be at least the clang
// that built the PBS archives (23.1.x).
const LLVMVersion = "23.1.5"

// LLVM returns an LLVM bin directory: dir if given, else PYTHAGO_LLVM, else the verified
// release fetched into cacheRoot/pythago-dist/llvm on first use (about 1 GB).
func LLVM(dir, cacheRoot string) (string, error) {
	if dir == "" {
		dir = os.Getenv("PYTHAGO_LLVM")
	}
	if dir == "" {
		var err error
		if dir, err = fetchLLVM(filepath.Join(cacheRoot, "pythago-dist", "llvm")); err != nil {
			return "", fmt.Errorf("LLVM (set PYTHAGO_LLVM to use an installed one): %w", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "clang")); err != nil {
		return "", fmt.Errorf("no clang in %s", dir)
	}
	return dir, nil
}

// Bao returns the bao executable to run instead of the bao linked in through go.mod: path
// if given, else PYTHAGO_BAO, else "" (in process).
func Bao(path string) string {
	if path == "" {
		path = os.Getenv("PYTHAGO_BAO")
	}
	return path
}

// fetchLLVM downloads the LLVM release for this host (verified against GitHub's digest)
// and returns its bin directory. On macOS, LLVM publishes arm64 only, and pythago-dist
// needs only clang there (the darwin glue).
func fetchLLVM(cache string) (string, error) {
	clang := []string{"bin/clang", "bin/clang-" + strings.Split(LLVMVersion, ".")[0], "lib/clang"}
	var name, ext string
	var tools []string
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64", runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		name = "LLVM-" + LLVMVersion + "-Linux-" + map[string]string{"arm64": "ARM64", "amd64": "X64"}[runtime.GOARCH]
		ext = ".tar.zst"
		tools = append(clang, "bin/lld", "bin/ld.lld", "bin/llvm-ar", "bin/llvm-nm", "bin/llvm-readobj", "bin/llvm-readelf")
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		// .tar.xz: macOS's bsdtar reads it natively.
		name, ext, tools = "LLVM-"+LLVMVersion+"-macOS-ARM64", ".tar.xz", clang
	default:
		return "", fmt.Errorf("no LLVM release for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	rel := ghrelease.Release{Repo: "llvm/llvm-project", Tag: "llvmorg-" + LLVMVersion, Cache: cache, Log: log.Printf}
	a, err := rel.Named(name + ext)
	if err != nil {
		return "", err
	}
	// Only what pythago-dist runs, and clang's headers and compiler-rt: about 750 MB of
	// the release's 13 GB.
	var members []string
	for _, m := range tools {
		members = append(members, name+"/"+m)
	}
	dir, err := rel.Extract(a, members...)
	if err != nil {
		return "", err
	}
	os.Remove(filepath.Join(cache, a.Name)) // the 1 GB archive; the extraction is what is kept
	return filepath.Join(dir, name, "bin"), nil
}
