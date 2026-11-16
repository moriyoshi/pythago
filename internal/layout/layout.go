// Package layout contains compiler-derived CPython layout definitions. These are
// ABI constants, not cached object metadata. Regenerate for each supported build.
package layout

type Key struct {
	Version      uint32
	FreeThreaded bool
}

type Unicode struct {
	Length, State, ASCIIData, CompactData, DataPointer uintptr
	KindByte, KindShift, KindMask                      uintptr
	CompactByte, CompactMask, ASCIIByte, ASCIIMask     uintptr
	BufferSize                                         uintptr
	BufferOffsets                                      [11]uintptr
}

var definitions = map[Key]Unicode{}

func Lookup(version uint32, freeThreaded bool) (Unicode, bool) {
	v, ok := definitions[Key{version, freeThreaded}]
	return v, ok
}
