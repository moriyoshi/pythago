// Package glue holds pythago's C glue, compiled into every native image by
// cmd/pythago-dist: PyStatus/PyConfig wrappers, the pthread wraps and the support code
// of package py (pythago_py.c).
package glue

import (
	"embed"
	"strings"
)

// Sources are the C files, and pythago_win32.h, which the Windows build includes.
//
//go:embed *.c *.h
var Sources embed.FS

// prebuilt holds glue objects for the shared-object build, one per Python ABI and
// architecture: the glue sources (except pythago_static.c) and their pythago_symtab,
// linked into one relocatable object. internal/tools/glueprebuilt builds them.
//
//go:embed all:prebuilt
var prebuilt embed.FS

// Key names a prebuilt glue object: the Python ABI ("3.13", "3.14t"), GOOS and GOARCH.
func Key(pyver, goos, goarch string) string {
	return "cp" + strings.ReplaceAll(pyver, ".", "") + "-" + goos + "_" + goarch
}

// PrebuiltDLL returns the prebuilt glue DLL for a Windows key, if there is one.
func PrebuiltDLL(key string) ([]byte, bool) {
	data, err := prebuilt.ReadFile("prebuilt/" + key + ".dll")
	return data, err == nil
}

// Prebuilt returns the prebuilt glue object for key, if there is one.
func Prebuilt(key string) ([]byte, bool) {
	data, err := prebuilt.ReadFile("prebuilt/" + key + ".o")
	return data, err == nil
}
