// Command statrepro shows Go interface dispatch going wrong in a binary that links
// the bao-generated libpython package, before any native function is called.
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"

	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

//go:embed all:app
var appFS embed.FS

func main() {
	app, _ := fs.Sub(appFS, "app")
	d, err := fs.ReadFile(app, "hello/data.txt")
	fmt.Printf("fs.ReadFile(sub)=%q,%v\n", d, err)
	d, err = appFS.ReadFile("app/hello/data.txt")
	fmt.Printf("embed.FS.ReadFile=%q,%v\n", d, err)
	if len(os.Args) > 1 {
		fmt.Printf("symtab at %#x\n", pyembed.SymbolAddr0())
	}
}
