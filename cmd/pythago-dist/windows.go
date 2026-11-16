package main

import (
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moriyoshi/pythago/glue"
	"github.com/moriyoshi/pythago/internal/pbs"
)

// runWindows generates a dist package for a Windows target. The native image there is
// not translated by bao: it is a set of files that package py extracts to a cache
// directory and loads with the Windows loader (py/image_windows.go), so that
// python3XY.dll is a module the loader knows and every extension module binds to it.
// The files: the PBS install_only build's python3XY.dll, python3.dll and C runtime DLLs,
// its DLLs/ directory (the standard library's extension modules, without test modules
// and tkinter), and the glue as pythago_glue.dll.
func runWindows(o *options) error {
	flavor := "install_only_stripped"
	meta := pbsMeta{version: o.python, target: o.triple}
	if o.variant == "freethreaded" {
		flavor = "freethreaded-" + flavor
		meta.abiTag = "t"
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
	dllName := "python" + strings.ReplaceAll(meta.pyver(), ".", "") + ".dll"
	if _, err := os.Stat(filepath.Join(py, dllName)); err != nil {
		return fmt.Errorf("no %s in %s", dllName, a.Name)
	}
	log.Printf("PBS: Python %s (%s), %s", meta.pyver(), o.triple, dllName)

	suffix := o.goos + "_" + o.goarch
	imgDir := filepath.Join(o.out, "image_"+suffix)
	if err := os.RemoveAll(imgDir); err != nil {
		return err
	}
	// python3.dll (python3t.dll) forwards the stable ABI to python3XY.dll for abi3
	// extension modules.
	for _, name := range []string{dllName, "python3.dll", "python3t.dll", "vcruntime140.dll", "vcruntime140_1.dll"} {
		src := filepath.Join(py, name)
		if _, err := os.Stat(src); err != nil {
			continue // only one of python3.dll and python3t.dll exists
		}
		if err := copyFile(src, filepath.Join(imgDir, name)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(py, "DLLs"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if (ext != ".pyd" && ext != ".dll") || strings.HasPrefix(name, "_test") ||
			strings.HasPrefix(name, "_tkinter") || strings.HasPrefix(name, "tcl") || strings.HasPrefix(name, "tk") ||
			name == "_ctypes_test.pyd" {
			continue
		}
		if err := copyFile(filepath.Join(py, "DLLs", name), filepath.Join(imgDir, "DLLs", name)); err != nil {
			return err
		}
	}
	glueDLL, err := windowsGlue(o, filepath.Join(py, "include"), filepath.Join(py, "libs"), meta)
	if err != nil {
		return err
	}
	if err := copyFile(glueDLL, filepath.Join(imgDir, "pythago_glue.dll")); err != nil {
		return err
	}
	id, err := treeDigest(imgDir)
	if err != nil {
		return err
	}
	if err := packStdlib(o, filepath.Join(py, "Lib"), "", meta); err != nil {
		return err
	}
	return emitWindows(o, py, meta, id)
}

// windowsGlue returns pythago_glue.dll for the target: the prebuilt one if package glue
// has it, else compiled with LLVM against a Windows SDK directory (-windows-sdk).
func windowsGlue(o *options, include, libs string, meta pbsMeta) (string, error) {
	key := glue.Key(meta.pyver(), o.goos, o.goarch)
	if err := os.MkdirAll(o.work, 0o755); err != nil {
		return "", err
	}
	if data, ok := glue.PrebuiltDLL(key); ok {
		log.Printf("glue: prebuilt %s", key)
		path := filepath.Join(o.work, "glue-"+key+".dll")
		return path, os.WriteFile(path, data, 0o644)
	}
	log.Printf("glue: no prebuilt DLL for %s; compiling", key)
	if err := o.needLLVM(); err != nil {
		return "", err
	}
	return compileWindowsGlue(o, include, libs, meta)
}

// compileWindowsGlue compiles the glue (MSVC ABI, clang) into pythago_glue.dll, linked
// against python3XY.lib and the C runtime's and kernel32's import libraries. The SDK
// directory holds msvcrt/ and windows/ headers and lib/<goarch>/ import libraries:
// Wine's, which ci/windows-wine.sh sdk copies out of its image (Microsoft's SDK is not
// redistributable).
// No CRT startup code is linked: the glue needs none, and it carries its own TLS
// directory (pythago_win32.h).
func compileWindowsGlue(o *options, include, libs string, meta pbsMeta) (string, error) {
	if o.windowsSDK == "" {
		return "", fmt.Errorf("compiling the Windows glue needs -windows-sdk (or PYTHAGO_WINDOWS_SDK): a directory with Wine's msvcrt/ and windows/ headers and lib/ import libraries (ci/windows-wine.sh sdk)")
	}
	sdk := o.windowsSDK
	dir := filepath.Join(o.work, "glue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	entries, _ := fs.ReadDir(glue.Sources, ".")
	for _, e := range entries {
		data, _ := fs.ReadFile(glue.Sources, e.Name())
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			return "", err
		}
	}
	target := map[string]string{"arm64": "aarch64-pc-windows-msvc", "amd64": "x86_64-pc-windows-msvc"}[o.goarch]
	machine := map[string]string{"arm64": "arm64", "amd64": "x64"}[o.goarch]
	resource, err := toolOutput(o.tool("clang"), "-print-resource-dir")
	if err != nil {
		return "", err
	}
	cflags := []string{"--target=" + target, "-nostdinc", "-isystem", filepath.Join(resource, "include"),
		"-isystem", filepath.Join(sdk, "msvcrt"), "-isystem", filepath.Join(sdk, "windows"),
		"-D_UCRT", "-D__WINE_PE_BUILD", "-fms-extensions", "-O2", "-I" + include, "-I" + filepath.Join(include, "internal")}
	if meta.abiTag == "t" {
		// Windows' pyconfig.h leaves the free-threaded ABI to the compiler's command line
		// (sysconfig passes it to extension builds); without it the structs are the GIL
		// build's, and pyconfig.h links python3XY.lib instead of python3XYt.lib.
		cflags = append(cflags, "-DPy_GIL_DISABLED=1")
	}
	var objs, names []string
	for _, name := range glueFiles(true) {
		obj := filepath.Join("glue", strings.TrimSuffix(name, ".c")+".obj")
		if err := execToolIn(o.work, o.tool("clang"), append(cflags, "-c", "-o", obj, filepath.Join("glue", name))...); err != nil {
			return "", err
		}
		n, err := coffFunctions(filepath.Join(o.work, obj), "pythago_")
		if err != nil {
			return "", err
		}
		objs, names = append(objs, obj), append(names, n...)
	}
	sort.Strings(names)
	var src strings.Builder
	src.WriteString("/* Generated by pythago-dist: pythago_symtab of the Windows glue DLL. */\n")
	src.WriteString("typedef struct { const char *name; void *addr; } pythago_symtab_entry;\n")
	for _, n := range names {
		fmt.Fprintf(&src, "extern char %s[];\n", n)
	}
	src.WriteString("__declspec(dllexport) const pythago_symtab_entry pythago_symtab[] = {\n")
	for _, n := range names {
		fmt.Fprintf(&src, "    {%q, %s},\n", n, n)
	}
	src.WriteString("    {0, 0},\n};\n")
	if err := os.WriteFile(filepath.Join(dir, "symtab.c"), []byte(src.String()), 0o644); err != nil {
		return "", err
	}
	symtab := filepath.Join("glue", "symtab.obj")
	if err := execToolIn(o.work, o.tool("clang"), append(cflags, "-c", "-o", symtab, filepath.Join("glue", "symtab.c"))...); err != nil {
		return "", err
	}
	out := filepath.Join("glue", "pythago_glue.dll")
	args := []string{"-flavor", "link", "/nologo", "/dll", "/noentry", "/machine:" + machine, "/out:" + out,
		"/brepro", "/libpath:" + libs}
	args = append(args, objs...)
	args = append(args, symtab)
	for _, l := range []string{"ucrtbase", "kernel32", "kernelbase", "vcruntime140"} {
		args = append(args, filepath.Join(sdk, "lib", o.goarch, "lib"+l+".a"))
	}
	if err := execToolIn(o.work, o.tool("lld"), args...); err != nil {
		return "", err
	}
	log.Printf("glue: pythago_glue.dll, pythago_symtab: %d symbols", len(names))
	return filepath.Join(o.work, out), nil
}

// coffFunctions lists a COFF object's external functions whose names start with prefix.
func coffFunctions(obj, prefix string) ([]string, error) {
	f, err := pe.Open(obj)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	for _, s := range f.Symbols {
		const classExternal, typeFunction = 2, 0x20
		if s.StorageClass == classExternal && s.SectionNumber > 0 && s.Type == typeFunction &&
			strings.HasPrefix(s.Name, prefix) {
			names = append(names, s.Name)
		}
	}
	return names, nil
}

// treeDigest names a directory's content: SHA-256 over its files' paths and bytes.
func treeDigest(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16], err
}

// emitWindows writes the registration file, the manifest and the license notice of a
// Windows dist package.
func emitWindows(o *options, py string, meta pbsMeta, id string) error {
	suffix := o.goos + "_" + o.goarch
	reg := fmt.Sprintf(`// Code generated by pythago-dist; DO NOT EDIT.

package %[1]s

import (
	"embed"
	"io/fs"

	"github.com/moriyoshi/pythago/py"
)

//go:embed stdlib_%[2]s.zip
var stdlibZip []byte

//go:embed all:image_%[2]s
var imageFiles embed.FS

// Importing this package registers CPython %[3]s (%[4]s) for %[5]s with package py: the
// native image's files, which py extracts to the user's cache directory and loads, and the
// standard library.
func init() {
	files, err := fs.Sub(imageFiles, "image_%[2]s")
	if err != nil {
		panic(err)
	}
	py.RegisterImageFiles(%[6]q, files)
	py.RegisterStdlibZip(stdlibZip)
}
`, o.pkg, suffix, meta.pyver(), o.variant, o.target, id)
	if err := os.WriteFile(filepath.Join(o.out, "register_"+suffix+".go"), []byte(reg), 0o644); err != nil {
		return err
	}
	if err := writeManifest(o, meta); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(py, "LICENSE.txt"), filepath.Join(o.out, "licenses", "LICENSE-python-windows.txt")); err != nil {
		return err
	}
	log.Printf("generated package %s in %s (image %s)", o.pkg, o.out, id)
	return nil
}
