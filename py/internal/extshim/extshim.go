//go:build linux

// Package extshim builds a code-free ELF shared object whose dynamic symbols are
// absolute (SHN_ABS) addresses inside the running process.
//
// Loading it with RTLD_GLOBAL makes symbols that are statically linked into the Go
// executable (the bao-translated libpython) visible to C extensions, which resolve
// their Py* references from the global scope. glibc honors SHN_ABS dynamic symbols
// since 2.28; both functions and data objects work because no code is involved.
package extshim

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Symbol is one exported absolute symbol.
type Symbol struct {
	Name string
	Addr uintptr
}

const (
	ehdrSize = 64
	phdrSize = 56
	symSize  = 24
	dynSize  = 16
	numPhdrs = 3
	pageSize = 0x10000 // Valid for 4, 16 and 64 KiB page kernels.
)

// Build returns the image of a shared object named soname exporting syms.
func Build(soname string, syms []Symbol) ([]byte, error) {
	var machine elf.Machine
	switch runtime.GOARCH {
	case "arm64":
		machine = elf.EM_AARCH64
	case "amd64":
		machine = elf.EM_X86_64
	default:
		return nil, fmt.Errorf("extshim: unsupported GOARCH %s", runtime.GOARCH)
	}
	syms = append([]Symbol(nil), syms...)
	sort.Slice(syms, func(i, j int) bool { return syms[i].Name < syms[j].Name })

	// .dynstr: leading NUL, the soname, then the symbol names.
	strtab := []byte{0}
	addStr := func(s string) uint32 {
		off := uint32(len(strtab))
		strtab = append(append(strtab, s...), 0)
		return off
	}
	sonameOff := addStr(soname)
	nameOff := make([]uint32, len(syms))
	for i, s := range syms {
		nameOff[i] = addStr(s.Name)
	}

	nsyms := len(syms) + 1 // Index 0 is the null symbol.
	nbucket := len(syms)/2 + 1
	symtabOff := align(ehdrSize+numPhdrs*phdrSize, 8)
	strtabOff := symtabOff + nsyms*symSize
	hashOff := align(strtabOff+len(strtab), 8)
	dynOff := align(hashOff+4*(2+nbucket+nsyms), 8)
	dyn := [][2]uint64{
		{uint64(elf.DT_HASH), uint64(hashOff)},
		{uint64(elf.DT_STRTAB), uint64(strtabOff)},
		{uint64(elf.DT_SYMTAB), uint64(symtabOff)},
		{uint64(elf.DT_STRSZ), uint64(len(strtab))},
		{uint64(elf.DT_SYMENT), symSize},
		{uint64(elf.DT_SONAME), uint64(sonameOff)},
		{uint64(elf.DT_NULL), 0},
	}
	size := dynOff + len(dyn)*dynSize
	b := make([]byte, size)
	le := binary.LittleEndian

	// ELF header.
	copy(b, elf.ELFMAG)
	b[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	b[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	b[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	b[elf.EI_OSABI] = byte(elf.ELFOSABI_NONE)
	le.PutUint16(b[16:], uint16(elf.ET_DYN))
	le.PutUint16(b[18:], uint16(machine))
	le.PutUint32(b[20:], uint32(elf.EV_CURRENT))
	le.PutUint64(b[32:], ehdrSize) // e_phoff
	le.PutUint16(b[52:], ehdrSize)
	le.PutUint16(b[54:], phdrSize)
	le.PutUint16(b[56:], numPhdrs)

	// Program headers. The single PT_LOAD is writable so glibc older than 2.35 can
	// relocate .dynamic entries in place.
	phdr := func(i int, typ elf.ProgType, flags elf.ProgFlag, off, sz, alignment int) {
		p := b[ehdrSize+i*phdrSize:]
		le.PutUint32(p[0:], uint32(typ))
		le.PutUint32(p[4:], uint32(flags))
		le.PutUint64(p[8:], uint64(off))  // p_offset
		le.PutUint64(p[16:], uint64(off)) // p_vaddr
		le.PutUint64(p[24:], uint64(off)) // p_paddr
		le.PutUint64(p[32:], uint64(sz))  // p_filesz
		le.PutUint64(p[40:], uint64(sz))  // p_memsz
		le.PutUint64(p[48:], uint64(alignment))
	}
	phdr(0, elf.PT_LOAD, elf.PF_R|elf.PF_W, 0, size, pageSize)
	phdr(1, elf.PT_DYNAMIC, elf.PF_R|elf.PF_W, dynOff, len(dyn)*dynSize, 8)
	// Without PT_GNU_STACK, glibc assumes the object needs an executable stack and
	// 2.41+ refuses to dlopen it.
	phdr(2, elf.PT_GNU_STACK, elf.PF_R|elf.PF_W, 0, 0, 16)

	// .dynsym.
	for i, s := range syms {
		p := b[symtabOff+(i+1)*symSize:]
		le.PutUint32(p[0:], nameOff[i])
		p[4] = elf.ST_INFO(elf.STB_GLOBAL, elf.STT_NOTYPE)
		p[5] = byte(elf.STV_DEFAULT)
		le.PutUint16(p[6:], uint16(elf.SHN_ABS))
		le.PutUint64(p[8:], uint64(s.Addr))
	}
	copy(b[strtabOff:], strtab)

	// SysV .hash: nbucket, nchain, buckets, chains.
	h := b[hashOff:]
	le.PutUint32(h[0:], uint32(nbucket))
	le.PutUint32(h[4:], uint32(nsyms))
	buckets, chains := h[8:], h[8+4*nbucket:]
	for i := len(syms); i >= 1; i-- {
		bk := sysvHash(syms[i-1].Name) % uint32(nbucket)
		le.PutUint32(chains[4*i:], le.Uint32(buckets[4*bk:]))
		le.PutUint32(buckets[4*bk:], uint32(i))
	}

	for i, d := range dyn {
		le.PutUint64(b[dynOff+i*dynSize:], d[0])
		le.PutUint64(b[dynOff+i*dynSize+8:], d[1])
	}
	return b, nil
}

// Source says where Load loads the shim from. The zero value loads it from a file under
// os.TempDir, or from a memfd if that fails.
type Source struct {
	// Dir is the directory under which a new temporary directory holds the shim's file
	// while it is loaded. If set, a failure is an error rather than a fallback to a memfd.
	Dir string
	// Memfd loads the shim from a memfd only.
	Memfd bool
}

// Load builds the shim and dlopens it RTLD_NOW|RTLD_GLOBAL from src.
//
// A file is removed as soon as it is loaded (the mapping outlives it), so the link map
// names a path that no longer exists. A memfd is loaded as /proc/self/fd/N, and that name
// stays in the link map: gdb opens every loaded object by its name, in its own process,
// where descriptor N is often one of gdb's pipes, and then blocks reading it forever with
// the program stopped.
func Load(soname string, syms []Symbol, src Source) (uintptr, error) {
	if src.Memfd && src.Dir != "" {
		return 0, fmt.Errorf("extshim: both a directory and a memfd given")
	}
	img, err := Build(soname, syms)
	if err != nil {
		return 0, err
	}
	if src.Memfd {
		return loadMemfd(soname, img, syms)
	}
	h, err := loadFile(src.Dir, soname, img, syms)
	if err == nil || src.Dir != "" {
		return h, err
	}
	return loadMemfd(soname, img, syms)
}

// loadFile loads img from a file named soname in a new temporary directory under parent
// (os.TempDir if empty), then removes both. The directory is new, so no loaded object has
// the path's name.
func loadFile(parent, soname string, img []byte, syms []Symbol) (uintptr, error) {
	dir, err := os.MkdirTemp(parent, "pythago-shim-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, soname)
	if err := os.WriteFile(path, img, 0o400); err != nil {
		return 0, err
	}
	return open(path, syms)
}

// loadMemfd loads img from a memfd as /proc/self/fd/N.
//
// glibc's dlopen returns an already-loaded object whose name matches the path, before
// opening the file. Another memfd-loaded module (bao's static-TLS stub, say) may have been
// loaded as /proc/self/fd/N from a descriptor since closed, and the shim's memfd can reuse
// N. So the shim moves to a descriptor whose path names no loaded object.
func loadMemfd(soname string, img []byte, syms []Symbol) (uintptr, error) {
	f, err := memfd(soname)
	if err != nil {
		return 0, err
	}
	defer f.Close() // The mapping outlives the descriptor.
	if _, err := f.Write(img); err != nil {
		return 0, err
	}
	const rtldNoload = 0x4
	fd := int(f.Fd())
	for tries := 0; ; tries++ {
		path := fmt.Sprintf("/proc/self/fd/%d", fd)
		if h, _ := purego.Dlopen(path, purego.RTLD_LAZY|rtldNoload); h == 0 {
			break
		} else {
			purego.Dlclose(h)
		}
		if tries == 64 {
			return 0, fmt.Errorf("extshim: no free /proc/self/fd path to load the shim from")
		}
		// Take the next descriptor number; the old one stays open until Load returns, so
		// the search moves forward.
		nfd, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_DUPFD_CLOEXEC, uintptr(fd+1))
		if errno != 0 {
			return 0, fmt.Errorf("extshim: dup: %w", errno)
		}
		fd = int(nfd)
		defer syscall.Close(fd)
	}
	return open(fmt.Sprintf("/proc/self/fd/%d", fd), syms)
}

// open dlopens the shim at path and checks that the handle is the shim's.
func open(path string, syms []Symbol) (uintptr, error) {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, err
	}
	if len(syms) > 0 {
		if _, err := purego.Dlsym(h, syms[0].Name); err != nil {
			return 0, fmt.Errorf("extshim: the loaded object is not the shim (no %s): %w", syms[0].Name, err)
		}
	}
	return h, nil
}

func memfd(name string) (*os.File, error) {
	nr := map[string]uintptr{"arm64": 279, "amd64": 319}[runtime.GOARCH]
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return nil, err
	}
	const mfdCloexec = 1
	fd, _, errno := syscall.Syscall(nr, uintptr(unsafe.Pointer(p)), mfdCloexec, 0)
	if errno != 0 {
		return nil, fmt.Errorf("memfd_create: %w", errno)
	}
	return os.NewFile(fd, name), nil
}

func sysvHash(name string) uint32 {
	var h uint32
	for i := 0; i < len(name); i++ {
		h = h<<4 + uint32(name[i])
		g := h & 0xf0000000
		h ^= g >> 24
		h &^= g
	}
	return h
}

func align(n, a int) int { return (n + a - 1) &^ (a - 1) }
