package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/moriyoshi/pythago/glue"
	"github.com/moriyoshi/pythago/internal/pbs"
)

// prebuiltTargets are the Python ABIs and architectures pythago ships glue objects for.
var prebuiltTargets = []struct{ python, variant, goos, goarch string }{
	{"3.13", "gil", "linux", "arm64"}, {"3.13", "gil", "linux", "amd64"},
	{"3.14", "freethreaded", "linux", "arm64"}, {"3.14", "freethreaded", "linux", "amd64"},
	{"3.13", "gil", "darwin", "arm64"}, {"3.13", "gil", "darwin", "amd64"},
	{"3.14", "freethreaded", "darwin", "arm64"}, {"3.14", "freethreaded", "darwin", "amd64"},
	{"3.13", "gil", "windows", "arm64"}, {"3.13", "gil", "windows", "amd64"},
	{"3.14", "freethreaded", "windows", "arm64"}, {"3.14", "freethreaded", "windows", "amd64"},
}

// darwinGlueSDK is the macOS SDK the darwin glue objects are compiled against. The objects
// target macOS 11 whatever the SDK; pinning it keeps them byte for byte reproducible.
const darwinGlueSDK = "26.5"

// prebuildGlue (maintainers) compiles the glue for the shared-object build for the
// prebuilt targets of this host's OS into dir as <key>.o, and updates their lines in
// dir/SHA256SUMS, keeping the other OS's. Linux targets take their sysroot from
// PYTHAGO_SYSROOT_<ARCH> (the host's too when set); darwin targets the pinned macOS SDK,
// which Apple licenses for Apple hardware, hence building them on a Mac. Windows targets
// (<key>.dll) are built on any host given -windows-sdk (ci/windows-wine.sh sdk).
// Compilation runs with the pinned LLVM on relative paths in a fixed layout, so the
// objects are reproducible: CI rebuilds them and compares SHA256SUMS.
func prebuildGlue(o *options, dir string) error {
	if err := o.needLLVM(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	built := map[string]bool{runtime.GOOS: true, "windows": o.windowsSDK != ""}
	sums, err := otherSums(filepath.Join(dir, "SHA256SUMS"), built)
	if err != nil {
		return err
	}
	var sdk string
	if runtime.GOOS == "darwin" {
		if sdk, err = pinnedSDK(o.sdk); err != nil {
			return err
		}
	}
	for _, t := range prebuiltTargets {
		if !built[t.goos] {
			continue
		}
		pyver := t.python
		flavor := "install_only_stripped"
		if t.variant == "freethreaded" {
			pyver, flavor = pyver+"t", "freethreaded-"+flavor
		}
		triple, err := pbs.Triple(t.goos, t.goarch)
		if err != nil {
			return err
		}
		rel := pbs.Release{Tag: o.release, Cache: o.cache, Log: log.Printf}
		a, err := rel.Find(t.python, triple, flavor)
		if err != nil {
			return err
		}
		pbsDir, err := rel.Extract(a)
		if err != nil {
			return err
		}
		to := *o
		// PYTHAGO_SYSROOT_<ARCH> applies to the host architecture too, so that every host
		// compiles against the same glibc headers (CI uses ci/sysroot.sh for both).
		to.goos, to.goarch, to.triple, to.sdk = t.goos, t.goarch, triple, sdk
		if t.goos == "linux" {
			to.sysroot = os.Getenv("PYTHAGO_SYSROOT_" + strings.ToUpper(t.goarch))
			if to.sysroot == "" && t.goarch != runtime.GOARCH {
				return fmt.Errorf("set PYTHAGO_SYSROOT_%s for the %s glue", strings.ToUpper(t.goarch), t.goarch)
			}
		}
		key := glue.Key(pyver, t.goos, t.goarch)
		// A fixed work directory and include path keep the objects byte-identical.
		to.work = filepath.Join(o.cacheRoot, "pythago-dist", "glue-build", key)
		os.RemoveAll(to.work)
		include := filepath.Join(to.work, "include")
		if err := os.MkdirAll(to.work, 0o755); err != nil {
			return err
		}
		pyInclude, name := filepath.Join(pbsDir, "python", "include", "python"+pyver), key+".o"
		if t.goos == "windows" {
			pyInclude, name = filepath.Join(pbsDir, "python", "include"), key+".dll"
		}
		if err := os.Symlink(pyInclude, include); err != nil {
			return err
		}
		var obj string
		if t.goos == "windows" {
			meta := pbsMeta{version: t.python, target: triple}
			if t.variant == "freethreaded" {
				meta.abiTag = "t"
			}
			obj, err = compileWindowsGlue(&to, "include", filepath.Join(pbsDir, "python", "libs"), meta)
		} else {
			obj, err = compileSharedGlue(&to, "include")
		}
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		data, err := os.ReadFile(obj)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		sums[name] = hex.EncodeToString(sum[:])
		log.Printf("glue %s: %d bytes", key, len(data))
	}
	var b strings.Builder
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(b.String()), 0o644)
}

// otherSums reads the SHA256SUMS lines of objects for the OSes a prebuild does not build,
// which it leaves as they are. A missing file has none.
func otherSums(path string, built map[string]bool) (map[string]string, error) {
	sums := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return sums, nil
	} else if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok {
			return nil, fmt.Errorf("%s: malformed line %q", path, line)
		}
		goos, _, _ := strings.Cut(name[strings.Index(name, "-")+1:], "_")
		if !built[goos] {
			sums[name] = sum
		}
	}
	return sums, nil
}

// pinnedSDK returns the macOS SDK directory for the darwin glue objects: sdk if given,
// else xcrun's macosx<darwinGlueSDK>, else the Command Line Tools' copy. Its
// SDKSettings.json must name darwinGlueSDK.
func pinnedSDK(sdk string) (string, error) {
	if sdk == "" {
		out, err := exec.Command("xcrun", "--sdk", "macosx"+darwinGlueSDK, "--show-sdk-path").Output()
		sdk = strings.TrimSpace(string(out))
		if err != nil || sdk == "" {
			sdk = "/Library/Developer/CommandLineTools/SDKs/MacOSX" + darwinGlueSDK + ".sdk"
		}
	}
	data, err := os.ReadFile(filepath.Join(sdk, "SDKSettings.json"))
	if err != nil {
		return "", fmt.Errorf("the darwin glue needs the macOS %s SDK (-sdk): %w", darwinGlueSDK, err)
	}
	var settings struct{ Version string }
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", fmt.Errorf("%s: %w", sdk, err)
	}
	if settings.Version != darwinGlueSDK {
		return "", fmt.Errorf("%s is the macOS %s SDK; the darwin glue is pinned to %s", sdk, settings.Version, darwinGlueSDK)
	}
	return sdk, nil
}

// compileSharedGlue compiles the glue sources (except pythago_static.c) and their
// pythago_symtab in o.work, with paths relative to it, and links them into one object.
func compileSharedGlue(o *options, include string) (string, error) {
	if o.goos == "darwin" {
		return compileDarwinGlue(o, include)
	}
	objs, names, err := compileGlue(o, include, glueFiles(true))
	if err != nil {
		return "", err
	}
	symtab, err := symtabObject(o, names)
	if err != nil {
		return "", err
	}
	out := filepath.Join(o.work, "glue.o")
	rel := []string{"-r", "-o", "glue.o"}
	for _, p := range append(objs, symtab) {
		r, err := filepath.Rel(o.work, p)
		if err != nil {
			return "", err
		}
		rel = append(rel, r)
	}
	cmd := exec.Command(o.tool("ld.lld"), rel...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = o.work, os.Stderr, os.Stderr
	return out, cmd.Run()
}
