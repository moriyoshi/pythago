package main

// The CPython native image and standard library, generated into ./pydist (not committed;
// about 60 MB per target). Run `go generate ./examples/hello` once, natively, or with
// GOARCH=amd64 PYTHAGO_SYSROOT=<amd64 glibc sysroot> for a cross target; targets
// accumulate in the one package. The toolchain (LLVM, bao) comes from PYTHAGO_LLVM /
// PYTHAGO_BAO, else is fetched into the user cache on first use.
//
// GOOS/GOARCH select the target here, but must not reach `go run`, which would build the
// generator itself for the target.
//
//go:generate env -u GOOS -u GOARCH go run github.com/moriyoshi/pythago/cmd/pythago-dist -python 3.13 -target $GOOS/$GOARCH -sysroot=$PYTHAGO_SYSROOT -o pydist
