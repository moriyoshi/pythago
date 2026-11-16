//go:build !windows

package py

// loadImage returns the registered native image's pythago_symtab (0 if none).
func loadImage() (uintptr, error) { return image.Load(), nil }

// imageSearchPaths lists directories of the native image that go on sys.path.
func imageSearchPaths() []string { return nil }
