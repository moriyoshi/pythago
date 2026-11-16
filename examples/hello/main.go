// Command hello is a single-file Go program with embedded Python: the interpreter, the
// standard library and the application's Python code all live in the binary.
//
//	go generate ./examples/hello && go build ./examples/hello && ./hello
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	_ "github.com/moriyoshi/pythago/examples/hello/pydist" // CPython native image + stdlib (go generate)
	"github.com/moriyoshi/pythago/py"
)

//go:embed all:app
var app embed.FS

func main() {
	files, _ := fs.Sub(app, "app")
	cfg := py.Config{
		Files: files,
		Modules: map[string]map[string]any{
			"host": {
				"shout": py.Func(func(args []*py.Object, _ py.Kwargs) (any, error) {
					s, err := args[0].GoString()
					return strings.ToUpper(s) + "!", err
				}),
			},
		},
	}
	py.Run(cfg, func() error {
		return py.Do(func() error {
			hello, err := py.Import("hello")
			if err != nil {
				return err
			}
			defer hello.Close()
			msg, err := hello.CallMethod("greet", "gopher")
			if err != nil {
				return err
			}
			defer msg.Close()
			s, err := msg.GoString()
			fmt.Println(s)
			return err
		})
	})
}
