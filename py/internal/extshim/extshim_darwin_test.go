package extshim

import (
	"debug/macho"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
)

// lookupTrie finds name (a Mach-O name) in an export trie the way dyld's trie walk does:
// at each node it takes the first child whose whole label prefixes what is left of the
// name, and stops with the node it reaches when nothing is left.
func lookupTrie(trie []byte, name string) (flags, addr uint64, ok bool) {
	uleb := func(p int) (uint64, int) {
		var v uint64
		for shift := 0; ; shift += 7 {
			c := trie[p]
			p++
			v |= uint64(c&0x7f) << shift
			if c&0x80 == 0 {
				return v, p
			}
		}
	}
	p := 0
	for {
		tsize, q := uleb(p)
		if name == "" {
			if tsize == 0 {
				return 0, 0, false
			}
			flags, q = uleb(q)
			addr, _ = uleb(q)
			return flags, addr, true
		}
		q += int(tsize)
		n := int(trie[q])
		q++
		next := -1
		for i := 0; i < n; i++ {
			end := q
			for trie[end] != 0 {
				end++
			}
			label := string(trie[q:end])
			off, r := uleb(end + 1)
			q = r
			if next < 0 && strings.HasPrefix(name, label) {
				next, name = int(off), name[len(label):]
			}
		}
		if next < 0 {
			return 0, 0, false
		}
		p = next
	}
}

// Names that prefix each other, share prefixes, or split an edge: a trie that is not a
// proper radix tree loses some of them to dyld's first-match walk.
var trieNames = []string{
	"Py", "PyErr", "PyErr_Clear", "PyErr_C", "PyErr_SetString", "PyA", "_Py_NoneStruct",
	"_Py", "PyLong_FromLong", "PyLong_FromLongLong", "PyLong_FromLongLongX",
}

func TestExportTrie(t *testing.T) {
	var syms []Symbol
	for i, n := range trieNames {
		syms = append(syms, Symbol{n, uintptr(0x1000_0000_0000 + i*0x111)})
	}
	img, err := Build("libtrie.dylib", syms)
	if err != nil {
		t.Fatal(err)
	}
	f, err := macho.NewFile(strings.NewReader(string(img)))
	if err != nil {
		t.Fatal(err)
	}
	var trie []byte
	for _, l := range f.Loads {
		raw := l.Raw()
		if binary.LittleEndian.Uint32(raw) == lcDyldInfoOnly {
			off, size := binary.LittleEndian.Uint32(raw[40:]), binary.LittleEndian.Uint32(raw[44:])
			trie = img[off : off+size]
		}
	}
	if trie == nil {
		t.Fatal("no LC_DYLD_INFO_ONLY")
	}
	for _, s := range syms {
		flags, addr, ok := lookupTrie(trie, "_"+s.Name)
		if !ok || flags != exportKindAbsolute || addr != uint64(s.Addr) {
			t.Errorf("_%s: ok=%v flags=%#x addr=%#x, want absolute %#x", s.Name, ok, flags, addr, s.Addr)
		}
	}
	for _, n := range []string{"_PyErr_", "_PyE", "_PyLong_FromLon", "PyErr", "_PyX"} {
		if _, _, ok := lookupTrie(trie, n); ok {
			t.Errorf("%s found, but was not exported", n)
		}
	}
	// The symbol table lists the same names, N_ABS, sorted (as dyld's binary search of
	// the external definitions wants).
	if f.Symtab == nil || len(f.Symtab.Syms) != len(syms) {
		t.Fatalf("symtab %v", f.Symtab)
	}
	for i, s := range f.Symtab.Syms {
		if i > 0 && f.Symtab.Syms[i-1].Name >= s.Name || s.Type != nAbs|nExt {
			t.Errorf("symbol %d: %+v", i, s)
		}
	}
}

var shimData = [4]uint64{0x5079746861676f}

// buildBundle compiles src into a bundle linked the way wheels' extension modules are
// (-undefined dynamic_lookup: flat-namespace binds), or skips without a C compiler.
func buildBundle(t *testing.T, dir, src string) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler to build a flat-namespace bundle with")
	}
	c := filepath.Join(dir, "probe.c")
	out := filepath.Join(dir, "probe.so")
	if err := os.WriteFile(c, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(cc, "-bundle", "-undefined", "dynamic_lookup", "-o", out, c).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, b)
	}
	return out
}

// imageNames lists the paths dyld's image list names.
func imageNames(t *testing.T) []string {
	var count func() uint32
	var name func(uint32) string
	purego.RegisterLibFunc(&count, purego.RTLD_DEFAULT, "_dyld_image_count")
	purego.RegisterLibFunc(&name, purego.RTLD_DEFAULT, "_dyld_get_image_name")
	var names []string
	for i := uint32(0); i < count(); i++ {
		names = append(names, name(i))
	}
	return names
}

// The shim dyld loads: its code signature verifies, dlsym(RTLD_DEFAULT) and a bundle's
// flat-namespace binds (a function call through the lazy pointer, a data object through
// the GOT) get the absolute addresses, and its file is gone once loaded.
func TestLoad(t *testing.T) {
	getpid, err := purego.Dlsym(purego.RTLD_DEFAULT, "getpid")
	if err != nil {
		t.Fatal(err)
	}
	syms := []Symbol{
		{"PyShimTest_GetPID", getpid},
		{"PyShimTest_Data", uintptr(unsafe.Pointer(&shimData))},
		{"PyShimTest_Data2", uintptr(unsafe.Pointer(&shimData[1]))},
	}
	dir := t.TempDir()

	img, err := Build("libshimtest.dylib", syms)
	if err != nil {
		t.Fatal(err)
	}
	signed := filepath.Join(dir, "libshimtest.dylib")
	if err := os.WriteFile(signed, img, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("codesign", "--verify", "--verbose=4", signed).CombinedOutput(); err != nil {
		t.Errorf("codesign --verify: %v\n%s", err, b)
	}

	before := map[string]bool{}
	for _, n := range imageNames(t) {
		before[n] = true
	}
	if _, err := Load("libshimtest.dylib", syms, Source{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 || ents[0].Name() != "libshimtest.dylib" {
		t.Errorf("%s holds %v; want only the file this test wrote", dir, ents)
	}
	var shim []string // the images Load added
	for _, n := range imageNames(t) {
		if !before[n] {
			shim = append(shim, n)
		}
	}
	real, err := filepath.EvalSymlinks(dir) // dyld names the real path: /private/var/...
	if err != nil {
		t.Fatal(err)
	}
	if len(shim) != 1 || !strings.HasPrefix(shim[0], filepath.Join(real, "pythago-shim-")) {
		t.Errorf("Load added the images %q, want one file in a new directory under %s", shim, real)
	}
	for _, s := range syms {
		if a, err := purego.Dlsym(purego.RTLD_DEFAULT, s.Name); err != nil || a != s.Addr {
			t.Errorf("dlsym(RTLD_DEFAULT, %s) = %#x, %v; want %#x", s.Name, a, err, s.Addr)
		}
	}

	bundle := buildBundle(t, dir, `
extern int PyShimTest_GetPID(void);
extern unsigned long long PyShimTest_Data[];
extern unsigned long long PyShimTest_Data2;
int probe_pid(void) { return PyShimTest_GetPID(); }
unsigned long long probe_data(void) { return PyShimTest_Data[0] + PyShimTest_Data2; }
`)
	h, err := purego.Dlopen(bundle, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		t.Fatalf("dlopen of a bundle that binds the shim's symbols: %v", err)
	}
	var probePID func() int32
	var probeData func() uint64
	purego.RegisterLibFunc(&probePID, h, "probe_pid")
	purego.RegisterLibFunc(&probeData, h, "probe_data")
	shimData[1] = 7
	if got, want := probePID(), int32(os.Getpid()); got != want {
		t.Errorf("probe_pid() = %d, want %d", got, want)
	}
	if got, want := probeData(), shimData[0]+7; got != want {
		t.Errorf("probe_data() = %#x, want %#x", got, want)
	}
}

func TestLoadSource(t *testing.T) {
	syms := []Symbol{{"PyShimTest_Source", 0x1234}}
	if _, err := Load("libshimsrc.dylib", syms, Source{Memfd: true}); err == nil || !strings.Contains(err.Error(), "memfd") {
		t.Errorf("Memfd: %v, want an error naming memfd", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Load("libshimsrc.dylib", syms, Source{Dir: missing}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing Dir: %v, want an error naming %s", err, missing)
	}
	if _, err := Build("libdup.dylib", []Symbol{{"PyA", 1}, {"PyA", 2}}); err == nil {
		t.Error("Build accepted a name twice")
	}
}

// BenchmarkBuild builds a shim the size of CPython 3.13's C API (1,754 names).
func BenchmarkBuild(b *testing.B) {
	var syms []Symbol
	for i := 0; i < 1754; i++ {
		syms = append(syms, Symbol{fmt.Sprintf("PyBench_%d_%x", i%37, i), uintptr(0x1_0000_0000 + 16*i)})
	}
	for b.Loop() {
		if _, err := Build("libbench.dylib", syms); err != nil {
			b.Fatal(err)
		}
	}
}
