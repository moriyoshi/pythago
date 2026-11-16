// Command nativelayout derives native layout definitions from CPython headers.
// It compiles ELF (or, for Windows targets, COFF) probes for the target and reads their
// data, so cross targets need not execute and C bitfields follow the actual target
// compiler ABI.
package main

import (
	"debug/elf"
	"debug/pe"
	"encoding/binary"
	"flag"
	"fmt"
	"go/format"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const source = `
#include <Python.h>
#include <stddef.h>
#include <stdint.h>
#if PY_VERSION_HEX < 0x030C0000
#error Native Unicode views require the post-wstr canonical representation
#endif
#ifdef Py_GIL_DISABLED
#define FREE_THREADED 1
#else
#define FREE_THREADED 0
#endif
const uint64_t pythago_layout[] = {
 PY_VERSION_HEX, FREE_THREADED,
 offsetof(PyASCIIObject, length), offsetof(PyASCIIObject, state),
 sizeof(PyASCIIObject), sizeof(PyCompactUnicodeObject), offsetof(PyUnicodeObject, data),
 sizeof(Py_buffer), offsetof(Py_buffer, buf), offsetof(Py_buffer, obj),
 offsetof(Py_buffer, len), offsetof(Py_buffer, itemsize), offsetof(Py_buffer, readonly),
 offsetof(Py_buffer, ndim), offsetof(Py_buffer, format), offsetof(Py_buffer, shape),
 offsetof(Py_buffer, strides), offsetof(Py_buffer, suboffsets), offsetof(Py_buffer, internal)
};
const PyASCIIObject pythago_kind = {.state.kind = 7};
const PyASCIIObject pythago_compact = {.state.compact = 1};
const PyASCIIObject pythago_ascii = {.state.ascii = 1};
`

func main() {
	clang := flag.String("clang", "clang", "target clang executable")
	include := flag.String("include", "", "CPython include directory containing Python.h")
	target := flag.String("target", "", "clang target triple")
	sysroot := flag.String("sysroot", "", "target libc sysroot")
	arch := flag.String("arch", "", "Go target architecture (amd64 or arm64)")
	out := flag.String("o", "", "generated Go file")
	cflags := flag.String("cflags", "", "further clang arguments, space-separated (for Windows targets: the C runtime and Windows header directories)")
	build := flag.String("build", "linux || darwin", "GOOS part of the generated file's build constraint")
	flag.Parse()
	if err := run(*clang, *include, *target, *sysroot, *arch, *out, strings.Fields(*cflags), *build); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(clang, include, target, sysroot, arch, out string, cflags []string, build string) error {
	if include == "" || target == "" || out == "" || (arch != "amd64" && arch != "arm64") {
		return fmt.Errorf("-include, -target, -arch and -o are required")
	}
	dir, err := os.MkdirTemp("", "pythago-layout-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	src, obj := filepath.Join(dir, "layout.c"), filepath.Join(dir, "layout.o")
	if err := os.WriteFile(src, []byte(source), 0600); err != nil {
		return err
	}
	args := []string{"--target=" + target, "-std=c11", "-O2", "-I" + include, "-c", src, "-o", obj}
	if sysroot != "" {
		args = append(args, "--sysroot="+sysroot)
	}
	args = append(args, cflags...)
	if data, err := exec.Command(clang, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("layout probe: %w\n%s", err, data)
	}
	var values map[string][]byte
	if strings.Contains(target, "windows") {
		values, err = coffValues(obj, arch)
	} else {
		values, err = elfValues(obj, arch)
	}
	if err != nil {
		return err
	}
	data := values["pythago_layout"]
	if len(data) != 19*8 {
		return fmt.Errorf("invalid layout probe size %d", len(data))
	}
	n := make([]uint64, 19)
	for i := range n {
		n[i] = binary.LittleEndian.Uint64(data[i*8:])
	}
	if values["pythago_kind"] == nil { // COFF has no symbol sizes: the probes are PyASCIIObjects
		for _, name := range []string{"pythago_kind", "pythago_compact", "pythago_ascii"} {
			values[name] = values[name+"@"][:n[4]]
		}
	}
	return generate(values, n, target, arch, build, out)
}

// elfValues returns the probe symbols' bytes.
func elfValues(obj, arch string) (map[string][]byte, error) {
	f, err := elf.Open(obj)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	expected := map[string]elf.Machine{"arm64": elf.EM_AARCH64, "amd64": elf.EM_X86_64}[arch]
	if f.Machine != expected {
		return nil, fmt.Errorf("ELF machine %v does not match %s", f.Machine, arch)
	}
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB {
		return nil, fmt.Errorf("unsupported target ELF format")
	}
	symbols, err := f.Symbols()
	if err != nil {
		return nil, err
	}
	values := map[string][]byte{}
	for _, s := range symbols {
		if !strings.HasPrefix(s.Name, "pythago_") {
			continue
		}
		if int(s.Section) >= len(f.Sections) {
			return nil, fmt.Errorf("invalid symbol section")
		}
		section := f.Sections[s.Section]
		data, err := section.Data()
		if err != nil {
			return nil, err
		}
		offset := s.Value - section.Addr
		if offset+s.Size > uint64(len(data)) {
			return nil, fmt.Errorf("invalid symbol size")
		}
		values[s.Name] = data[offset : offset+s.Size]
	}
	return values, nil
}

// coffValues returns the probe symbols' bytes from a COFF object. COFF symbols have no
// size: pythago_layout gets its 19 words, the others the rest of their section, under
// name+"@" (run trims them to sizeof(PyASCIIObject)).
func coffValues(obj, arch string) (map[string][]byte, error) {
	f, err := pe.Open(obj)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	expected := map[string]uint16{"arm64": pe.IMAGE_FILE_MACHINE_ARM64, "amd64": pe.IMAGE_FILE_MACHINE_AMD64}[arch]
	if f.Machine != expected {
		return nil, fmt.Errorf("COFF machine %#x does not match %s", f.Machine, arch)
	}
	values := map[string][]byte{}
	for _, s := range f.Symbols {
		if !strings.HasPrefix(s.Name, "pythago_") || s.SectionNumber <= 0 || int(s.SectionNumber) > len(f.Sections) {
			continue
		}
		data, err := f.Sections[s.SectionNumber-1].Data()
		if err != nil {
			return nil, err
		}
		if int(s.Value) > len(data) {
			return nil, fmt.Errorf("invalid symbol offset")
		}
		rest := data[s.Value:]
		if s.Name == "pythago_layout" {
			if len(rest) < 19*8 {
				return nil, fmt.Errorf("invalid layout probe size %d", len(rest))
			}
			values[s.Name] = rest[:19*8]
		} else {
			values[s.Name+"@"] = rest
		}
	}
	return values, nil
}

// generate writes the definition read from the probes.
func generate(values map[string][]byte, n []uint64, target, arch, build, out string) error {
	field := func(name string) (int, int, error) {
		found, mask := -1, 0
		for i, v := range values[name] {
			if v == 0 {
				continue
			}
			if found >= 0 {
				return 0, 0, fmt.Errorf("%s spans bytes", name)
			}
			found, mask = i, int(v)
		}
		if found < 0 {
			return 0, 0, fmt.Errorf("missing probe %s", name)
		}
		return found, mask, nil
	}
	kind, km, err := field("pythago_kind")
	if err != nil {
		return err
	}
	compact, cm, err := field("pythago_compact")
	if err != nil {
		return err
	}
	ascii, am, err := field("pythago_ascii")
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by internal/tools/nativelayout; DO NOT EDIT.\n// CPython PY_VERSION_HEX=%#x, free-threaded=%t, target=%s.\n//go:build (%s) && %s\n\npackage layout\n\nfunc init() {\n definitions[Key{Version: %#x, FreeThreaded: %t}] = Unicode{\n", n[0], n[1] != 0, target, build, arch, n[0], n[1] != 0)
	fmt.Fprintf(&b, "Length: %d, State: %d, ASCIIData: %d, CompactData: %d, DataPointer: %d,\n", n[2], n[3], n[4], n[5], n[6])
	fmt.Fprintf(&b, "KindByte: %d, KindShift: %d, KindMask: %#x, CompactByte: %d, CompactMask: %#x, ASCIIByte: %d, ASCIIMask: %#x,\n", kind, bits.TrailingZeros8(uint8(km)), km, compact, cm, ascii, am)
	fmt.Fprintf(&b, "BufferSize: %d, BufferOffsets: [11]uintptr{", n[7])
	for _, v := range n[8:] {
		fmt.Fprintf(&b, "%d,", v)
	}
	b.WriteString("},\n}\n}\n")
	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return err
	}
	return os.WriteFile(out, formatted, 0644)
}
