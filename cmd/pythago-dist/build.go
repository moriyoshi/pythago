package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/moriyoshi/bao/build"
	"github.com/moriyoshi/pythago/glue"
)

// Modules left out of the inittab: interactive terminal, GUI/dbm, and test-only modules.
var dropModules = []string{"readline", "_curses", "_curses_panel", "xxsubtype", "_xxtestfuzz", "_testinternalcapi"}

// Archive members that belong to dropped modules or to the python executable.
var dropMembers = []string{"python.o", "config.o", "readline.o", "_cursesmodule.o", "_curses_panel.o",
	"xxsubtype.o", "_xxtestfuzz.o", "fuzzer.o", "_testinternalcapi.o", "test_lock.o",
	"test_critical_sections.o", "_tkinter.o", "tkappinit.o", "_dbmmodule.o"}

// Static dependency archives still referenced after the drops (zstd only from 3.14).
var deps = []string{"ssl", "crypto", "ffi", "sqlite3", "z", "bz2", "lzma", "mpdec", "expat", "uuid", "zstd"}

type pbsMeta struct {
	version, abiTag, target, glibcFloor string
}

// pyver is e.g. "3.13", or "3.14t" for a free-threaded build.
func (m pbsMeta) pyver() string { return m.version + m.abiTag }

// floorNote describes the glibc floor for logs; darwin targets have none.
func (m pbsMeta) floorNote() string {
	if m.glibcFloor == "" {
		return ""
	}
	return ", glibc floor " + m.glibcFloor
}

func readMeta(pbs string) (pbsMeta, error) {
	data, err := os.ReadFile(filepath.Join(pbs, "PYTHON.json"))
	if err != nil {
		return pbsMeta{}, err
	}
	var j struct {
		Version     string   `json:"python_major_minor_version"`
		ABITag      string   `json:"python_abi_tag"`
		Target      string   `json:"target_triple"`
		CRTFeatures []string `json:"crt_features"`
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return pbsMeta{}, err
	}
	m := pbsMeta{version: j.Version, abiTag: j.ABITag, target: j.Target, glibcFloor: minGlibc}
	for _, f := range j.CRTFeatures {
		if v, ok := strings.CutPrefix(f, "glibc-max-symbol-version:"); ok && versionLess(minGlibc, v) {
			m.glibcFloor = v
		}
	}
	return m, nil
}

// minGlibc is the oldest glibc pythago supports: bao's native-TLS runtime needs
// __cxa_thread_atexit_impl (GLIBC_2.18). The image's floor is the newer of this and the
// archive's glibc-max-symbol-version (2.17 for current PBS builds), so on older glibc the
// loader refuses the binary at startup ("version GLIBC_2.18 not found").
const minGlibc = "2.18"

// versionLess compares dotted versions numerically.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}

func (o *options) clangTarget() []string {
	if o.goos == "darwin" {
		arch := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[o.goarch]
		// macOS 11 is PBS's deployment target (and bao's default -min-os).
		return []string{"--target=" + arch + "-apple-macos11", "-isysroot", o.sdk}
	}
	args := []string{"--target=" + o.triple}
	if o.sysroot != "" {
		args = append(args, "--sysroot="+o.sysroot)
	}
	return args
}

func (o *options) tool(name string) string { return filepath.Join(o.llvm, name) }

// buildArchive produces libpython.a for bao: the LTO-lowered libpython, wrapped for the
// pthread hooks, plus the glue and pythago_symtab.
func buildArchive(o *options, pbs string, meta pbsMeta) (string, error) {
	pyver := meta.pyver()
	include := filepath.Join(pbs, "install", "include", "python"+pyver)
	configDirs, _ := filepath.Glob(filepath.Join(pbs, "install", "lib", "python"+pyver, "config-"+pyver+"-*"))
	if len(configDirs) != 1 {
		return "", fmt.Errorf("cannot find config-%s-* in the PBS install", pyver)
	}
	// LTO lowering takes minutes: reuse the lowered object when its inputs are unchanged.
	lowered := filepath.Join(o.work, "libpython.o")
	key, err := lowerKey(o, pbs, configDirs[0], pyver)
	if err != nil {
		return "", err
	}
	old, _ := os.ReadFile(lowered + ".key")
	if _, err := os.Stat(lowered); err == nil && (o.skipLTO || string(old) == key) {
		log.Printf("reuse %s", lowered)
	} else {
		os.Remove(lowered + ".key")
		if err := lower(o, pbs, configDirs[0], include, pyver, lowered); err != nil {
			return "", err
		}
		if err := os.WriteFile(lowered+".key", []byte(key), 0o644); err != nil {
			return "", err
		}
	}

	// The glue, wrapped together with libpython: libpython's pthread_create, _join,
	// _detach and _exit calls reach the glue's __wrap_* functions, and the glue's
	// __real_* calls reach libc. Threads started by dependencies stay plain pthreads.
	glueObjs, glueNames, err := compileGlue(o, include, glueFiles(false))
	if err != nil {
		return "", err
	}
	wrapped := filepath.Join(o.work, "libpython-wrapped.o")
	args := append([]string{"-r", "-o", wrapped, lowered}, glueObjs...)
	for _, w := range wrappedSymbols {
		args = append(args, "--wrap="+w)
	}
	log.Printf("wrap %s", strings.Join(wrappedSymbols, " "))
	if err := execTool(o.tool("ld.lld"), args...); err != nil {
		return "", err
	}
	names, err := exportedSymbols(wrapped, func(s elf.Symbol) bool {
		bind, typ := elf.ST_BIND(s.Info), elf.ST_TYPE(s.Info)
		return (bind == elf.STB_GLOBAL || bind == elf.STB_WEAK) && elf.ST_VISIBILITY(s.Other) == elf.STV_DEFAULT &&
			s.Section != elf.SHN_UNDEF && (typ == elf.STT_FUNC || typ == elf.STT_OBJECT) && pyName.MatchString(s.Name)
	})
	if err != nil {
		return "", err
	}
	symtabO, err := symtabObject(o, append(names, glueNames...))
	if err != nil {
		return "", err
	}
	archive := filepath.Join(o.work, "libpython.a")
	os.Remove(archive)
	return archive, execTool(o.tool("llvm-ar"), "rcs", archive, wrapped, symtabO)
}

// wrappedSymbols are the libpython imports the glue takes over.
var wrappedSymbols = []string{"pthread_create", "pthread_join", "pthread_detach", "pthread_exit"}

// glueFiles lists the glue sources for a build: pythago_static.c only for the static
// (LTO) build, whose libpython lacks what glibc's libc_nonshared.a supplied to PBS.
func glueFiles(shared bool) []string {
	entries, _ := fs.ReadDir(glue.Sources, ".")
	var files []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".c") || shared && e.Name() == "pythago_static.c" {
			continue
		}
		files = append(files, e.Name())
	}
	return files
}

// compileGlue compiles glue sources against the Python headers in include, and returns
// the objects and their pythago_* functions (which join pythago_symtab).
func compileGlue(o *options, include string, files []string) (objs, names []string, err error) {
	for _, name := range files {
		src := filepath.Join(o.work, "glue", name)
		data, _ := fs.ReadFile(glue.Sources, name)
		if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(src, data, 0o644); err != nil {
			return nil, nil, err
		}
		obj := strings.TrimSuffix(src, ".c") + ".o"
		// Relative paths, run in o.work: the objects do not depend on where they are built.
		args := append(o.clangTarget(), "-c", "-O2", "-fPIC", "-I"+include, "-I"+filepath.Join(include, "internal"),
			"-o", filepath.Join("glue", strings.TrimSuffix(name, ".c")+".o"), filepath.Join("glue", name))
		if o.goarch == "arm64" {
			args = append(args, "-mno-outline-atomics") // no compiler-rt helpers to link
		}
		if err := execToolIn(o.work, o.tool("clang"), args...); err != nil {
			return nil, nil, err
		}
		n, err := exportedSymbols(obj, func(s elf.Symbol) bool {
			return elf.ST_BIND(s.Info) == elf.STB_GLOBAL && s.Section != elf.SHN_UNDEF &&
				elf.ST_TYPE(s.Info) == elf.STT_FUNC && strings.HasPrefix(s.Name, "pythago_")
		})
		if err != nil {
			return nil, nil, err
		}
		objs, names = append(objs, obj), append(names, n...)
	}
	return objs, names, nil
}

// symtabObject compiles pythago_symtab: {name, address} of each symbol, NULL-terminated.
// Go reads the glue's (and, in the static build, the C API's) addresses through it.
func symtabObject(o *options, names []string) (string, error) {
	var c bytes.Buffer
	c.WriteString("/* Generated by pythago-dist. */\n")
	for _, n := range names {
		fmt.Fprintf(&c, "extern char %s[];\n", n)
	}
	c.WriteString("const struct { const char *name; void *addr; } pythago_symtab[] = {\n")
	for _, n := range names {
		fmt.Fprintf(&c, "  {%q, %s},\n", n, n)
	}
	c.WriteString("  {0, 0},\n};\n")
	symtabC := filepath.Join(o.work, "symtab.c")
	if err := os.WriteFile(symtabC, c.Bytes(), 0o644); err != nil {
		return "", err
	}
	symtabO := filepath.Join(o.work, "symtab.o")
	log.Printf("pythago_symtab: %d symbols", len(names))
	return symtabO, execToolIn(o.work, o.tool("clang"), append(o.clangTarget(), "-c", "-O2", "-fPIC", "-w", "-o", "symtab.o", "symtab.c")...)
}

var pyName = regexp.MustCompile(`^_?Py`)

func exportedSymbols(obj string, keep func(elf.Symbol) bool) ([]string, error) {
	f, err := elf.Open(obj)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	syms, err := f.Symbols()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range syms {
		if keep(s) && !seen[s.Name] {
			seen[s.Name] = true
			out = append(out, s.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// lowerKey identifies the inputs of lower: the archive, the inittab, the extra objects,
// what is dropped, the target, and the LLVM version.
func lowerKey(o *options, pbs, configDir, pyver string) (string, error) {
	h := sha256.New()
	files := []string{filepath.Join(configDir, "libpython"+pyver+".a"), filepath.Join(configDir, "config.c"),
		filepath.Join(pbs, "PYTHON.json")}
	hacl, _ := filepath.Glob(filepath.Join(pbs, "build", "Modules", "_hacl", "*.o"))
	sort.Strings(hacl)
	for _, f := range append(files, hacl...) {
		in, err := os.Open(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n", filepath.Base(f))
		_, err = io.Copy(h, in)
		in.Close()
		if err != nil {
			return "", err
		}
	}
	version, err := exec.Command(o.tool("clang"), "--version").Output()
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "%s|%v|%v|%s", o.triple, dropModules, dropMembers, version)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// lower extracts libpython's members (bitcode in the pgo+lto archives), drops what the
// inittab no longer lists, adds objects the archive lacks, regenerates the inittab, and
// LTO-lowers everything into one relocatable object.
func lower(o *options, pbs, configDir, include, pyver, out string) error {
	members := filepath.Join(o.work, "members")
	os.RemoveAll(members)
	if err := os.MkdirAll(members, 0o755); err != nil {
		return err
	}
	if err := extractMembers(filepath.Join(configDir, "libpython"+pyver+".a"), members, dropMembers); err != nil {
		return err
	}
	// Member names are not unique (Python/pytime.o vs a test module's pytime.o): keep
	// only the core one.
	pytimes, _ := filepath.Glob(filepath.Join(members, "*_pytime.o"))
	for _, m := range pytimes {
		if !defines(o, m, "PyTime_Time") {
			os.Remove(m)
		}
	}
	// libpython.a omits some objects PYTHON.json lists for built-in extensions
	// (3.13: Modules/_hacl/Hacl_Hash_SHA2.o).
	hacl, _ := filepath.Glob(filepath.Join(pbs, "build", "Modules", "_hacl", "*.o"))
	for _, h := range hacl {
		if m, _ := filepath.Glob(filepath.Join(members, "*_"+filepath.Base(h))); len(m) == 0 {
			data, err := os.ReadFile(h)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(members, "extra_"+filepath.Base(h)), data, 0o644); err != nil {
				return err
			}
		}
	}
	// The inittab without the dropped modules.
	src, err := os.ReadFile(filepath.Join(configDir, "config.c"))
	if err != nil {
		return err
	}
	decl := regexp.MustCompile(`^\s*extern PyObject\* PyInit_(\w+)\(void\);`)
	entry := regexp.MustCompile(`^\s*\{"(\w+)", PyInit_\w+\},`)
	var cfg bytes.Buffer
	for _, line := range strings.SplitAfter(string(src), "\n") {
		m := decl.FindStringSubmatch(line)
		if m == nil {
			m = entry.FindStringSubmatch(line)
		}
		if m != nil && slices.Contains(dropModules, m[1]) {
			continue
		}
		cfg.WriteString(line)
	}
	configC := filepath.Join(o.work, "config.c")
	if err := os.WriteFile(configC, cfg.Bytes(), 0o644); err != nil {
		return err
	}
	configO := filepath.Join(o.work, "config.o")
	if err := execTool(o.tool("clang"), append(o.clangTarget(), "-c", "-O2", "-fPIC", "-ftls-model=global-dynamic",
		"-I"+include, "-DPy_BUILD_CORE", "-o", configO, configC)...); err != nil {
		return err
	}
	objs, _ := filepath.Glob(filepath.Join(members, "*.o"))
	sort.Strings(objs)
	log.Printf("LTO-lower %d members (takes a few minutes)", len(objs))
	return execTool(o.tool("ld.lld"), append([]string{"-r", "--lto-O2", "-o", out, configO}, objs...)...)
}

func defines(o *options, obj, sym string) bool {
	out, err := exec.Command(o.tool("llvm-nm"), "--defined-only", "--format=just-symbols", obj).Output()
	if err != nil {
		return false
	}
	return slices.Contains(strings.Fields(string(out)), sym)
}

// extractMembers writes each member of a System V (GNU) ar archive under an index prefix,
// since member names repeat.
func extractMembers(archive, dst string, drop []string) error {
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(data, []byte("!<arch>\n")) {
		return fmt.Errorf("%s: not an ar archive", archive)
	}
	var names []byte
	idx := 0
	for off := 8; off+60 <= len(data); {
		hdr := data[off : off+60]
		name := strings.TrimSpace(string(hdr[:16]))
		size, err := strconv.Atoi(strings.TrimSpace(string(hdr[48:58])))
		if err != nil {
			return fmt.Errorf("%s: bad member header at %d", archive, off)
		}
		body := data[off+60 : off+60+size]
		off += 60 + size + size&1
		switch {
		case name == "//":
			names = body
			continue
		case name == "/" || name == "/SYM64/":
			continue
		case strings.HasPrefix(name, "/"):
			start, err := strconv.Atoi(name[1:])
			if err != nil {
				continue
			}
			end := bytes.Index(names[start:], []byte("/\n"))
			name = string(names[start : start+end])
		default:
			name = strings.TrimSuffix(name, "/")
		}
		if name == "" || slices.Contains(drop, name) {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, fmt.Sprintf("%03d_%s", idx, name)), body, 0o644); err != nil {
			return err
		}
		idx++
	}
	return nil
}

// runBao translates libpython.a, its static dependencies and the compiler support library
// into the output package.
func runBao(o *options, pbs string, meta pbsMeta, archive string) error {
	bo := build.Options{Input: archive, Linux: build.LinuxOptions{MaxTextAlignment: 4096}}
	for _, d := range deps {
		lib := filepath.Join(pbs, "build", "lib", "lib"+d+".a")
		if _, err := os.Stat(lib); err == nil {
			bo.LinkArchives = append(bo.LinkArchives, lib)
		}
	}
	// Compiler support routines: outline-atomics helpers from compiler-rt on arm64; libgcc
	// from the sysroot on amd64 (__cpu_model, 128-bit division). An LLVM built for another
	// host (an amd64 machine making an arm64 image) has no aarch64 compiler-rt: libgcc from
	// the sysroot has the same helpers.
	var builtins []string
	if o.goarch == "arm64" {
		builtins, _ = filepath.Glob(filepath.Join(o.llvm, "..", "lib", "clang", "*", "lib", "aarch64-*linux-gnu", "libclang_rt.builtins.a"))
		if len(builtins) == 0 {
			builtins, _ = filepath.Glob(filepath.Join(o.sysrootOrRoot(), "usr", "lib", "gcc", "aarch64-linux-gnu", "*", "libgcc.a"))
		}
	} else {
		builtins, _ = filepath.Glob(filepath.Join(o.sysrootOrRoot(), "usr", "lib", "gcc", "x86_64-linux-gnu", "*", "libgcc.a"))
	}
	if len(builtins) == 0 {
		return fmt.Errorf("no compiler support library (compiler-rt builtins or libgcc.a) found")
	}
	bo.LinkArchives = append(bo.LinkArchives, builtins[len(builtins)-1])
	// bao runs <triple>-ld to combine archive inputs; ld.lld does the job when binutils for
	// the target are not installed.
	if ld := map[string]string{"amd64": "x86_64-linux-gnu-ld", "arm64": "aarch64-linux-gnu-ld"}[o.goarch]; o.goarch != runtime.GOARCH {
		if _, err := exec.LookPath(ld); err != nil {
			bo.Linker = o.tool("ld.lld")
		}
	}
	return baoBuild(o, meta, bo)
}

func (o *options) sysrootOrRoot() string {
	if o.sysroot != "" {
		return o.sysroot
	}
	return "/"
}

// baoBuild runs bao build on bo with the common settings: in process through bao's build
// package (the version go.mod pins), or with the executable from -bao / PYTHAGO_BAO. It then
// moves the output into the package directory.
func baoBuild(o *options, meta pbsMeta, bo build.Options) error {
	bo.Target, bo.Package = o.goos+"/"+o.goarch, o.pkg
	bo.Symbols = []string{"pythago_symtab"}
	if o.goos == "darwin" {
		bo.SDK = o.sdk // empty: bao asks xcrun
	} else {
		bo.Sysroot = o.sysrootOrRoot()
		bo.Linux.MaxSymbolVersions = []string{"GLIBC_" + meta.glibcFloor}
		bo.Linux.StaticTLS = o.staticTLS
	}
	bo.Output = filepath.Join(o.work, "bao-out")
	bo.Stdout, bo.Stderr = os.Stderr, os.Stderr
	if err := os.RemoveAll(bo.Output); err != nil {
		return err
	}
	if o.bao != "" {
		log.Printf("%s build -target %s", o.bao, bo.Target)
		cmd := exec.Command(o.bao, append([]string{"build"}, bo.CommandLine()...)...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("bao: %w", err)
		}
	} else {
		log.Printf("bao build -target %s (%s)", bo.Target, baoVersion())
		if err := build.Run(context.Background(), bo); err != nil {
			return fmt.Errorf("bao: %w", err)
		}
	}
	return installTargetFiles(o, bo.Output)
}

// baoVersion is the bao module version linked into pythago-dist.
func baoVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, m := range bi.Deps {
			if m.Path == "github.com/moriyoshi/bao" {
				if m.Replace != nil {
					return m.Version + " => " + m.Replace.Path
				}
				return m.Version
			}
		}
	}
	return "unknown version"
}

// installTargetFiles moves bao's output for this target into the output package, which
// may already hold other targets: Go and object files carry _<goos>_<goarch> suffixes, and
// bao's JSON reports get one.
func installTargetFiles(o *options, baoOut string) error {
	suffix := "_" + o.goos + "_" + o.goarch
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(o.out, "*"+suffix+".*"))
	for _, f := range old {
		os.Remove(f)
	}
	entries, err := os.ReadDir(baoOut)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if ext := filepath.Ext(name); ext == ".json" {
			name = strings.TrimSuffix(name, ext) + suffix + ext
		} else if !strings.Contains(name, suffix) {
			return fmt.Errorf("unexpected bao output %s", name)
		}
		if err := os.Rename(filepath.Join(baoOut, e.Name()), filepath.Join(o.out, name)); err != nil {
			return err
		}
	}
	return nil
}
