package extshim

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"runtime"
	"sort"
)

// Mach-O constants (mach-o/loader.h, mach-o/nlist.h).
const (
	mhMagic64            = 0xfeedfacf
	mhDylib              = 6
	mhDyldLink           = 0x4
	mhTwoLevel           = 0x80
	mhNoReexportedDylibs = 0x100000

	lcSymtab           = 0x2
	lcDysymtab         = 0xb
	lcIDDylib          = 0xd
	lcSegment64        = 0x19
	lcCodeSignature    = 0x1d
	lcDyldInfoOnly     = 0x80000022
	lcBuildVersion     = 0x32
	platformMacOS      = 1
	macOS11            = 11 << 16
	vmProtRead         = 1
	vmProtExecute      = 4
	nExt               = 0x1
	nAbs               = 0x2
	exportKindAbsolute = 2 // EXPORT_SYMBOL_FLAGS_KIND_ABSOLUTE

	machHeaderSize = 32
	segmentSize    = 72
	nlistSize      = 16
)

// Build returns the image of a dylib whose install name is installName and whose export
// trie lists each of syms (by its C name; the trie holds the Mach-O name, with a leading
// underscore) as an absolute (EXPORT_SYMBOL_FLAGS_KIND_ABSOLUTE) address. The image holds
// no code: one r-x __TEXT page with the header and load commands, and __LINKEDIT with the
// trie, an N_ABS symbol table (for nm and debuggers) and an ad-hoc code signature. dyld
// refuses a __TEXT that is not r-x, even with no code in it, and arm64 macOS maps no
// unsigned executable page.
func Build(installName string, syms []Symbol) ([]byte, error) {
	var cpu, subtype uint32
	var page int
	switch runtime.GOARCH {
	case "arm64":
		cpu, subtype, page = 0x0100000c, 0, 0x4000 // CPU_TYPE_ARM64, CPU_SUBTYPE_ARM64_ALL
	case "amd64":
		cpu, subtype, page = 0x01000007, 3, 0x1000 // CPU_TYPE_X86_64, CPU_SUBTYPE_X86_64_ALL
	default:
		return nil, fmt.Errorf("extshim: unsupported GOARCH %s", runtime.GOARCH)
	}
	syms = append([]Symbol(nil), syms...)
	sort.Slice(syms, func(i, j int) bool { return syms[i].Name < syms[j].Name })
	for i := 1; i < len(syms); i++ {
		if syms[i].Name == syms[i-1].Name {
			return nil, fmt.Errorf("extshim: %s exported twice", syms[i].Name)
		}
	}
	trie, err := exportTrie(syms)
	if err != nil {
		return nil, err
	}

	// __LINKEDIT: the export trie, the symbol table, the string table, the signature.
	strtab := []byte{' ', 0}
	strx := make([]uint32, len(syms))
	for i, s := range syms {
		strx[i] = uint32(len(strtab))
		strtab = append(append(append(strtab, '_'), s.Name...), 0)
	}
	textSize := page
	exportOff := textSize
	symOff := align(exportOff+len(trie), 8)
	strOff := symOff + len(syms)*nlistSize
	sigOff := align(strOff+len(strtab), 16)
	strSize := sigOff - strOff // padded, as ld64 does, so the signature follows directly
	ident := installName
	sigSize := signatureSize(sigOff, ident)
	size := sigOff + sigSize
	linkeditSize := size - textSize

	b := make([]byte, size)
	le := binary.LittleEndian
	cmds := b[machHeaderSize:]
	ncmds := 0
	cmd := func(typ uint32, n int) []byte {
		c := cmds[:n]
		le.PutUint32(c[0:], typ)
		le.PutUint32(c[4:], uint32(n))
		cmds = cmds[n:]
		ncmds++
		return c
	}
	segment := func(name string, vmaddr, vmsize, fileoff, filesize int, prot uint32) {
		c := cmd(lcSegment64, segmentSize)
		copy(c[8:24], name)
		le.PutUint64(c[24:], uint64(vmaddr))
		le.PutUint64(c[32:], uint64(vmsize))
		le.PutUint64(c[40:], uint64(fileoff))
		le.PutUint64(c[48:], uint64(filesize))
		le.PutUint32(c[56:], prot) // maxprot
		le.PutUint32(c[60:], prot) // initprot
	}
	segment("__TEXT", 0, textSize, 0, textSize, vmProtRead|vmProtExecute)
	segment("__LINKEDIT", textSize, align(linkeditSize, page), textSize, linkeditSize, vmProtRead)

	c := cmd(lcIDDylib, align(24+len(installName)+1, 8))
	le.PutUint32(c[8:], 24)       // name offset
	le.PutUint32(c[12:], 1)       // timestamp
	le.PutUint32(c[16:], 0x10000) // current version 1.0.0
	le.PutUint32(c[20:], 0x10000) // compatibility version 1.0.0
	copy(c[24:], installName)

	c = cmd(lcDyldInfoOnly, 48) // no rebases or binds: only exports
	le.PutUint32(c[40:], uint32(exportOff))
	le.PutUint32(c[44:], uint32(len(trie)))

	c = cmd(lcSymtab, 24)
	le.PutUint32(c[8:], uint32(symOff))
	le.PutUint32(c[12:], uint32(len(syms)))
	le.PutUint32(c[16:], uint32(strOff))
	le.PutUint32(c[20:], uint32(strSize))

	c = cmd(lcDysymtab, 80)                 // every symbol is an external definition
	le.PutUint32(c[16:], 0)                 // iextdefsym
	le.PutUint32(c[20:], uint32(len(syms))) // nextdefsym
	le.PutUint32(c[24:], uint32(len(syms))) // iundefsym

	c = cmd(lcBuildVersion, 24)
	le.PutUint32(c[8:], platformMacOS)
	le.PutUint32(c[12:], macOS11) // minos
	le.PutUint32(c[16:], macOS11) // sdk

	c = cmd(lcCodeSignature, 16)
	le.PutUint32(c[8:], uint32(sigOff))
	le.PutUint32(c[12:], uint32(sigSize))

	sizeofcmds := len(b) - machHeaderSize - len(cmds)
	if machHeaderSize+sizeofcmds > textSize {
		return nil, fmt.Errorf("extshim: load commands overflow __TEXT")
	}
	le.PutUint32(b[0:], mhMagic64)
	le.PutUint32(b[4:], cpu)
	le.PutUint32(b[8:], subtype)
	le.PutUint32(b[12:], mhDylib)
	le.PutUint32(b[16:], uint32(ncmds))
	le.PutUint32(b[20:], uint32(sizeofcmds))
	le.PutUint32(b[24:], mhDyldLink|mhTwoLevel|mhNoReexportedDylibs)

	copy(b[exportOff:], trie)
	for i, s := range syms {
		p := b[symOff+i*nlistSize:]
		le.PutUint32(p[0:], strx[i])
		p[4] = nAbs | nExt
		p[5] = 0 // NO_SECT
		le.PutUint64(p[8:], uint64(s.Addr))
	}
	copy(b[strOff:], strtab)
	sign(b[sigOff:], b[:sigOff], ident, textSize)
	return b, nil
}

// exportTrie encodes syms (sorted, distinct) as a Mach-O export trie of absolute symbols.
// dyld walks it edge by edge, taking the first child whose whole label prefixes what is
// left of the name, and never backtracks: a name under a later sibling of an edge that
// prefixes it (_PyErr_Clear beside _PyErr) is not found. A radix tree, in which no two
// children of a node share a first byte, has no such siblings.
func exportTrie(syms []Symbol) ([]byte, error) {
	type node struct {
		labels   []string
		children []*node
		terminal bool
		addr     uint64
		off      int
	}
	root := &node{}
	for _, s := range syms {
		name := "_" + s.Name
		n := root
	walk:
		for {
			for i, l := range n.labels {
				k := 0
				for k < len(l) && k < len(name) && l[k] == name[k] {
					k++
				}
				switch {
				case k == 0:
					continue
				case k == len(l): // the whole edge: descend
					n, name = n.children[i], name[k:]
					continue walk
				default: // split the edge at k
					mid := &node{labels: []string{l[k:]}, children: []*node{n.children[i]}}
					n.labels[i], n.children[i] = l[:k], mid
					n, name = mid, name[k:]
					continue walk
				}
			}
			break
		}
		if name == "" {
			n.terminal, n.addr = true, uint64(s.Addr)
			continue
		}
		n.labels = append(n.labels, name)
		n.children = append(n.children, &node{terminal: true, addr: uint64(s.Addr)})
	}

	var nodes []*node // preorder: a node's children follow it
	var collect func(*node) error
	collect = func(n *node) error {
		if len(n.children) > 255 {
			return fmt.Errorf("extshim: an export trie node has %d children", len(n.children))
		}
		nodes = append(nodes, n)
		for _, c := range n.children {
			if err := collect(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(root); err != nil {
		return nil, err
	}
	terms := make([][]byte, len(nodes)) // flags and address of each terminal node
	for i, n := range nodes {
		if n.terminal {
			terms[i] = appendUleb(appendUleb(nil, exportKindAbsolute), n.addr)
		}
	}
	// Child offsets are ULEB128, so a node's size depends on the offsets after it: lay
	// out until nothing moves (offsets only grow, so this ends).
	for changed := true; changed; {
		changed = false
		off := 0
		for i, n := range nodes {
			if n.off != off {
				n.off, changed = off, true
			}
			t := len(terms[i])
			off += ulebLen(uint64(t)) + t + 1
			for i, l := range n.labels {
				off += len(l) + 1 + ulebLen(uint64(n.children[i].off))
			}
		}
	}
	var out []byte
	for i, n := range nodes {
		t := terms[i]
		out = appendUleb(out, uint64(len(t)))
		out = append(out, t...)
		out = append(out, byte(len(n.children)))
		for i, l := range n.labels {
			out = append(append(out, l...), 0)
			out = appendUleb(out, uint64(n.children[i].off))
		}
	}
	return out, nil
}

// Code signature (the ad-hoc, linker-signed form Go's linker writes; see
// cmd/internal/codesign): a SuperBlob holding one CodeDirectory with a SHA-256 hash of
// each 4 KiB page before the signature.
const (
	csMagicEmbeddedSignature = 0xfade0cc0
	csMagicCodeDirectory     = 0xfade0c02
	csSlotCodeDirectory      = 0
	csAdhoc                  = 0x2
	csLinkerSigned           = 0x20000
	csHashTypeSHA256         = 2
	csPageBits               = 12
	csPageSize               = 1 << csPageBits
	superBlobSize            = 12
	blobIndexSize            = 8
	codeDirectorySize        = 88 // version 0x20400, through execSegFlags
)

func signatureSize(codeSize int, ident string) int {
	nhashes := (codeSize + csPageSize - 1) / csPageSize
	return superBlobSize + blobIndexSize + codeDirectorySize + len(ident) + 1 + nhashes*sha256.Size
}

// sign writes the signature of code, whose first execSize bytes are the executable
// segment, to out.
func sign(out, code []byte, ident string, execSize int) {
	be := binary.BigEndian
	size := signatureSize(len(code), ident)
	nhashes := (len(code) + csPageSize - 1) / csPageSize
	be.PutUint32(out[0:], csMagicEmbeddedSignature)
	be.PutUint32(out[4:], uint32(size))
	be.PutUint32(out[8:], 1) // count
	be.PutUint32(out[12:], csSlotCodeDirectory)
	be.PutUint32(out[16:], superBlobSize+blobIndexSize)
	cd := out[superBlobSize+blobIndexSize:]
	identOff := codeDirectorySize
	hashOff := identOff + len(ident) + 1
	be.PutUint32(cd[0:], csMagicCodeDirectory)
	be.PutUint32(cd[4:], uint32(size-superBlobSize-blobIndexSize))
	be.PutUint32(cd[8:], 0x20400) // version: with the exec segment fields
	be.PutUint32(cd[12:], csAdhoc|csLinkerSigned)
	be.PutUint32(cd[16:], uint32(hashOff))
	be.PutUint32(cd[20:], uint32(identOff))
	be.PutUint32(cd[24:], 0) // nSpecialSlots
	be.PutUint32(cd[28:], uint32(nhashes))
	be.PutUint32(cd[32:], uint32(len(code))) // codeLimit
	cd[36] = sha256.Size
	cd[37] = csHashTypeSHA256
	cd[39] = csPageBits
	// spare2, scatterOffset, teamOffset, spare3, codeLimit64 stay zero.
	be.PutUint64(cd[64:], 0)                // execSegBase
	be.PutUint64(cd[72:], uint64(execSize)) // execSegLimit
	be.PutUint64(cd[80:], 0)                // execSegFlags: not a main binary
	copy(cd[identOff:], ident)
	for i := 0; i < nhashes; i++ {
		page := code[i*csPageSize : min((i+1)*csPageSize, len(code))]
		h := sha256.Sum256(page)
		copy(cd[hashOff+i*sha256.Size:], h[:])
	}
}

func appendUleb(b []byte, v uint64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

func ulebLen(v uint64) int { return len(appendUleb(nil, v)) }

func align(n, a int) int { return (n + a - 1) &^ (a - 1) }
