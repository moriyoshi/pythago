// Package extshim builds a code-free Mach-O dylib whose export trie lists absolute
// (EXPORT_SYMBOL_FLAGS_KIND_ABSOLUTE) addresses inside the running process.
//
// The image's libpython was translated by bao and mapped by its loader, so dyld never
// loaded it and knows none of its symbols. Extension modules on macOS are linked with
// -undefined dynamic_lookup: their Py* references are flat-namespace binds, which dyld
// resolves by searching every image loaded RTLD_GLOBAL. Loading this dylib that way
// gives those binds, and dlsym(RTLD_DEFAULT), the live addresses. dyld binds an absolute
// export to its value as is, so functions and data objects both work.
package extshim

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ebitengine/purego"
)

// Symbol is one exported absolute symbol, by its C name (no leading underscore).
type Symbol struct {
	Name string
	Addr uintptr
}

// Source says where Load loads the shim from. The zero value loads it from a file under
// os.TempDir.
type Source struct {
	// Dir is the directory under which a new temporary directory holds the shim's file
	// while it is loaded.
	Dir string
	// Memfd is unsupported: dyld loads only files (it maps the file and attaches its code
	// signature by descriptor), and macOS has no memfd. Load fails if it is set.
	Memfd bool
}

// Load builds the shim and dlopens it RTLD_NOW|RTLD_GLOBAL from a file in a new
// temporary directory under src.Dir (os.TempDir if empty), then removes both: dyld's
// mapping outlives the file, and its image list keeps naming the removed path (the real
// path: /private/var/..., not /var/...). Every Load writes a new file, and macOS checks a
// code file it has not seen before on its first load: dlopen takes about 0.3 s on an M1
// Max and 0.4 s on an i9 (macOS 26), where a file loaded before takes well under 1 ms.
func Load(installName string, syms []Symbol, src Source) (uintptr, error) {
	if src.Memfd {
		return 0, fmt.Errorf("extshim: macOS has no memfd: the shim loads from a file")
	}
	img, err := Build(installName, syms)
	if err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp(src.Dir, "pythago-shim-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, installName)
	if err := os.WriteFile(path, img, 0o400); err != nil {
		return 0, err
	}
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return 0, err
	}
	if len(syms) > 0 {
		a, err := purego.Dlsym(h, syms[0].Name)
		if err != nil {
			return 0, fmt.Errorf("extshim: the loaded dylib does not export %s: %w", syms[0].Name, err)
		}
		if a != syms[0].Addr {
			return 0, fmt.Errorf("extshim: dyld resolved %s to %#x, not its absolute address %#x", syms[0].Name, a, syms[0].Addr)
		}
	}
	return h, nil
}
