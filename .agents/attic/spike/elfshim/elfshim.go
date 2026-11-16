// Package elfshim builds a code-free ELF shared object whose dynamic symbols are
// absolute (SHN_ABS) addresses inside the running process.
//
// Loading it with RTLD_GLOBAL makes symbols that are statically linked into the Go
// executable (the bao-translated libpython) visible to C extensions, which resolve
// their Py* references from the global scope. glibc honors SHN_ABS dynamic symbols
// since 2.28; both functions and data objects work because no code is involved.
package elfshim

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
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
		return nil, fmt.Errorf("elfshim: unsupported GOARCH %s", runtime.GOARCH)
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

// Load builds the shim, writes it to a memfd and dlopens it RTLD_NOW|RTLD_GLOBAL.
func Load(soname string, syms []Symbol) (uintptr, error) {
	img, err := Build(soname, syms)
	if err != nil {
		return 0, err
	}
	f, err := memfd(soname)
	if err != nil {
		return 0, err
	}
	defer f.Close() // The mapping outlives the descriptor.
	if _, err := f.Write(img); err != nil {
		return 0, err
	}
	return purego.Dlopen(fmt.Sprintf("/proc/self/fd/%d", f.Fd()), purego.RTLD_NOW|purego.RTLD_GLOBAL)
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
