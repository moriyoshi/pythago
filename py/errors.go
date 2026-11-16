package py

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unsafe"
)

// Error is a Python exception.
type Error struct {
	Type      string // the exception's type, qualified by its module unless builtin
	Message   string // str(exception)
	Traceback string // the traceback as Python prints it
	exc       *Object
	exit      int  // SystemExit's status
	isExit    bool // the exception is a SystemExit
	exitPrint bool // its code is a message to print on exit
}

// SystemExit reports whether the exception is a SystemExit (sys.exit()) and the exit
// status Python would use: 0 for a None code, the integer code, or 1 for any other value,
// which python prints to stderr ([Run] does too).
func (e *Error) SystemExit() (status int, ok bool) { return e.exit, e.isExit }

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Type
	}
	return e.Type + ": " + e.Message
}

// Exception returns a new reference to the exception object.
func (e *Error) Exception() *Object { return e.exc.NewRef() }

// fetchError takes the raised exception, clearing Python's error indicator.
func fetchError() error {
	p := call0(cErrGetRaised)
	if p == 0 {
		return errors.New("py: C API call failed without setting an exception")
	}
	e := &Error{exc: own(p)}
	if cls := quiet(callStr1(cObjectGetAttrString, p, "__class__")); cls != nil {
		e.Type = quietStr(callStr1(cObjectGetAttrString, cls.p, "__qualname__"))
		if mod := quietStr(callStr1(cObjectGetAttrString, cls.p, "__module__")); mod != "" && mod != "builtins" {
			e.Type = mod + "." + e.Type
		}
		cls.Close()
	}
	e.Message = quietStr(call1(cObjectStr, p))
	if e.Type == "SystemExit" {
		e.isExit, e.exit = true, 1
		if code := quiet(callStr1(cObjectGetAttrString, p, "code")); code != nil {
			switch int32(call1(cPyKind, code.p)) {
			case kindNone:
				e.exit = 0
			case kindInt, kindBool:
				if n, err := code.Int64(); err == nil {
					e.exit = int(n)
				} else {
					call0(cErrClear)
				}
			default:
				e.exitPrint = true // sys.exit("message")
			}
			code.Close()
		}
	}
	if tb := quiet(callStr0(cImportImportModule, "traceback")); tb != nil {
		if f := quiet(callStr1(cObjectGetAttrString, tb.p, "format_exception")); f != nil {
			argv := [2]uintptr{p}
			if lines := quiet(call4(cObjectVectorcallDict, f.p, uintptr(unsafe.Pointer(&argv[0])), 1, 0)); lines != nil {
				if v, err := lines.Go(); err == nil {
					var b strings.Builder
					for _, l := range v.([]any) {
						s, _ := l.(string)
						b.WriteString(s)
					}
					e.Traceback = b.String()
				}
				lines.Close()
			}
			f.Close()
		}
		tb.Close()
	}
	return e
}

// quiet wraps a new reference, or clears the error when p is NULL; for best-effort
// lookups while building an Error.
func quiet(p uintptr) *Object {
	if p == 0 {
		call0(cErrClear)
		return nil
	}
	return own(p)
}

func quietStr(p uintptr) string {
	o := quiet(p)
	if o == nil {
		return ""
	}
	defer o.Close()
	var n int64
	s := call2(cUnicodeAsUTF8AndSize, o.p, uintptr(unsafe.Pointer(&n)))
	if s == 0 {
		call0(cErrClear)
		return ""
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(s)), n))
}

// raise sets err as the current Python exception: a Python exception is re-raised, any
// other error becomes RuntimeError.
func raise(err error) {
	var pe *Error
	if errors.As(err, &pe) && pe.exc != nil && pe.exc.p != 0 {
		incRef(pe.exc.p)
		call1(cErrSetRaised, pe.exc.p) // steals
		return
	}
	p := cString(err.Error())
	call2(cErrSetString, cExcRuntimeError, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

func panicError(v any) error { return fmt.Errorf("py: Go function panicked: %v", v) }
