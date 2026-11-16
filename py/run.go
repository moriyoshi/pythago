package py

import (
	"runtime"
	"unsafe"
)

// Start symbols for Py_CompileString.
const (
	fileInput = 257
	evalInput = 258
)

// Exec runs Python statements in the namespace of module __main__.
func Exec(src string) error {
	g, err := mainDict()
	if err != nil {
		return err
	}
	defer g.Close()
	r, err := run(src, fileInput, g)
	r.Close()
	return err
}

// Eval evaluates a Python expression in the namespace of module __main__.
func Eval(expr string) (*Object, error) {
	g, err := mainDict()
	if err != nil {
		return nil, err
	}
	defer g.Close()
	return run(expr, evalInput, g)
}

// ExecIn runs Python statements with globals as their namespace (a dict).
func ExecIn(src string, globals *Object) error {
	r, err := run(src, fileInput, globals)
	r.Close()
	return err
}

// Import imports a module.
func Import(name string) (*Object, error) { return result(callStr0(cImportImportModule, name)) }

func run(src string, start uintptr, globals *Object) (*Object, error) {
	s, f := cString(src), cString("<pythago>")
	code, err := result(call3(cCompileString, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(f)), start))
	runtime.KeepAlive(s)
	runtime.KeepAlive(f)
	if err != nil {
		return nil, err
	}
	defer code.Close()
	return result(call3(cEvalEvalCode, code.p, globals.p, globals.p))
}

func mainDict() (*Object, error) {
	m, err := result(callStr0(cImportAddModuleRef, "__main__"))
	if err != nil {
		return nil, err
	}
	defer m.Close()
	return newRef(call1(cModuleGetDict, m.p)), nil
}
