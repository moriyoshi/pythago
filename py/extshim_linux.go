package py

import (
	"regexp"

	"github.com/moriyoshi/pythago/py/internal/extshim"
)

// exported matches the C API names the extension shim exports. The binary also carries
// OpenSSL, sqlite, zlib, libffi and others; exporting those would let wheels' vendored
// copies bind to ours.
var exported = regexp.MustCompile(`^_?Py`)

// loadExtensionShim makes the statically linked C API visible to C extensions, which
// resolve their Py* references from the global symbol scope: a code-free ELF of SHN_ABS
// symbols at the live addresses, loaded RTLD_GLOBAL from where cfg says.
func loadExtensionShim(cfg Config) error {
	var syms []extshim.Symbol
	for name, addr := range symbols {
		if exported.MatchString(name) {
			syms = append(syms, extshim.Symbol{Name: name, Addr: addr})
		}
	}
	src := extshim.Source{Dir: cfg.ExtensionShimDir, Memfd: cfg.ExtensionShimMemfd}
	_, err := extshim.Load("libpython-pythago.so", syms, src)
	return err
}
