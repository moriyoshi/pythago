package py

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// C API addresses, resolved from pythago_symtab at Init. Calls go through call
// (ccall.go); the few functions with floating-point arguments or results use
// purego.RegisterFunc.
var (
	cIncRef, cDecRef                                     uintptr
	cSaveThread, cRestoreThread, cFinalizeEx             uintptr
	cErrGetRaised, cErrSetRaised, cErrSetString          uintptr
	cErrOccurred, cErrClear                              uintptr
	cObjectStr, cObjectRepr, cObjectGetAttrString        uintptr
	cObjectSetAttrString, cObjectGetItem, cObjectSetItem uintptr
	cObjectSize, cObjectVectorcallDict, cObjectIsTrue    uintptr
	cLongFromLongLong, cLongFromUnsignedLongLong         uintptr
	cLongAsLongLongAndOverflow, cLongFromString          uintptr
	cUnicodeFromStringAndSize, cUnicodeAsUTF8AndSize     uintptr
	cUnicodeGetLength, cUnicodeReadChar                  uintptr
	cUnicodeAsUCS4, cUnicodeSubstring                    uintptr
	cBytesFromStringAndSize, cBytesAsStringAndSize       uintptr
	cByteArrayAsString, cByteArraySize                   uintptr
	cListNew, cListSetItem, cListSize, cListGetItemRef   uintptr
	cTupleNew, cTupleSetItem, cTupleSize, cTupleGetItem  uintptr
	cDictNew, cDictSetItem, cDictItems                   uintptr
	cCompileString, cEvalEvalCode, cImportAddModuleRef   uintptr
	cModuleGetDict, cImportImportModule, cModuleNew      uintptr
	cImportGetModuleDict, cDictSetItemString             uintptr
	cCMethodNew, cMemRawCalloc, cMemRawFree              uintptr
	cObjectGetBuffer, cBufferRelease                     uintptr
	cGoBufNew, cGoBufReleased, cGoBufDrain, cPyBuild     uintptr
	cPyABI, cPyCall, cMemoryViewFromObject               uintptr

	cConfigNew, cConfigFree, cConfigSetHome, cConfigSetProgramName uintptr
	cConfigAppendSearchPath, cInitializeCore, cInitializeMain      uintptr
	cConfigSetExecutable, cConfigSetArgv, cRunMain                 uintptr
	cPyInstallModule, cConfigSetSignals, cPyFsInstall              uintptr
	cPySetSpawn, cPySetLimit                                       uintptr
	cPyStarted, cPyStopping, cPyDepth, cPyEnter, cPyLeave          uintptr
	cPyRelease, cPyReacquire, cPyThreadMain, cPySetCallback        uintptr
	cPyFastcall, cPyKind, cPyGILDisabled, cPyStats                 uintptr
	cGILStateGetThisThreadState                                    uintptr

	cNone, cTrue, cFalse, cExcRuntimeError uintptr

	floatFromDouble func(float64) uintptr
	floatAsDouble   func(uintptr) float64
)

// glueABI is the glue's PYTHAGO_PY_ABI this package is written for: the layout of
// callFrame and pyValue, and of the fusion's structures (internal/fusion's cell and
// cConfig). 4: every argument and keyword name is decoded, into memory C passes by
// pointer. 5: the fusion (Config.FusedBytes).
const glueABI = 5

// symbols maps every pythago_symtab name to its address; the extension shim exports the
// C API (^_?Py) part of it.
var symbols map[string]uintptr

type symtabEntry struct {
	name *byte
	addr uintptr
}

// resolve reads the NULL-terminated pythago_symtab at base, adds the registered
// exports of a shared libpython, and binds every C API address package py uses.
func resolve(base uintptr) error {
	syms := map[string]uintptr{}
	for e := (*symtabEntry)(unsafe.Pointer(base)); e.name != nil; e = (*symtabEntry)(unsafe.Add(unsafe.Pointer(e), unsafe.Sizeof(*e))) {
		syms[goString(e.name)] = e.addr
	}
	if x := exports.Load(); x != nil {
		for _, name := range x.names {
			if addr, ok := x.lookup(name); ok {
				syms[name] = addr
			}
		}
	}
	for _, s := range []struct {
		p    *uintptr
		name string
	}{
		{&cIncRef, "Py_IncRef"}, {&cDecRef, "Py_DecRef"},
		{&cSaveThread, "PyEval_SaveThread"}, {&cRestoreThread, "PyEval_RestoreThread"},
		{&cFinalizeEx, "Py_FinalizeEx"},
		{&cErrGetRaised, "PyErr_GetRaisedException"}, {&cErrSetRaised, "PyErr_SetRaisedException"},
		{&cErrSetString, "PyErr_SetString"}, {&cErrOccurred, "PyErr_Occurred"}, {&cErrClear, "PyErr_Clear"},
		{&cObjectStr, "PyObject_Str"}, {&cObjectRepr, "PyObject_Repr"},
		{&cObjectGetAttrString, "PyObject_GetAttrString"}, {&cObjectSetAttrString, "PyObject_SetAttrString"},
		{&cObjectGetItem, "PyObject_GetItem"}, {&cObjectSetItem, "PyObject_SetItem"},
		{&cObjectSize, "PyObject_Size"}, {&cObjectVectorcallDict, "PyObject_VectorcallDict"},
		{&cObjectIsTrue, "PyObject_IsTrue"},
		{&cLongFromLongLong, "PyLong_FromLongLong"}, {&cLongFromUnsignedLongLong, "PyLong_FromUnsignedLongLong"},
		{&cLongAsLongLongAndOverflow, "PyLong_AsLongLongAndOverflow"}, {&cLongFromString, "PyLong_FromString"},
		{&cUnicodeFromStringAndSize, "PyUnicode_FromStringAndSize"}, {&cUnicodeAsUTF8AndSize, "PyUnicode_AsUTF8AndSize"},
		{&cUnicodeGetLength, "PyUnicode_GetLength"}, {&cUnicodeReadChar, "PyUnicode_ReadChar"},
		{&cUnicodeAsUCS4, "PyUnicode_AsUCS4"}, {&cUnicodeSubstring, "PyUnicode_Substring"},
		{&cBytesFromStringAndSize, "PyBytes_FromStringAndSize"}, {&cBytesAsStringAndSize, "PyBytes_AsStringAndSize"},
		{&cByteArrayAsString, "PyByteArray_AsString"}, {&cByteArraySize, "PyByteArray_Size"},
		{&cListNew, "PyList_New"}, {&cListSetItem, "PyList_SetItem"}, {&cListSize, "PyList_Size"},
		{&cListGetItemRef, "PyList_GetItemRef"},
		{&cTupleNew, "PyTuple_New"}, {&cTupleSetItem, "PyTuple_SetItem"}, {&cTupleSize, "PyTuple_Size"},
		{&cTupleGetItem, "PyTuple_GetItem"},
		{&cDictNew, "PyDict_New"}, {&cDictSetItem, "PyDict_SetItem"}, {&cDictItems, "PyDict_Items"},
		{&cDictSetItemString, "PyDict_SetItemString"},
		{&cCompileString, "Py_CompileString"}, {&cEvalEvalCode, "PyEval_EvalCode"},
		{&cImportAddModuleRef, "PyImport_AddModuleRef"}, {&cModuleGetDict, "PyModule_GetDict"},
		{&cImportImportModule, "PyImport_ImportModule"}, {&cModuleNew, "PyModule_New"},
		{&cImportGetModuleDict, "PyImport_GetModuleDict"},
		{&cCMethodNew, "PyCMethod_New"}, {&cMemRawCalloc, "PyMem_RawCalloc"}, {&cMemRawFree, "PyMem_RawFree"},
		{&cObjectGetBuffer, "PyObject_GetBuffer"}, {&cBufferRelease, "PyBuffer_Release"},
		{&cGoBufNew, "pythago_gobuf_new"}, {&cGoBufReleased, "pythago_gobuf_released"},
		{&cGoBufDrain, "pythago_gobuf_drain"},

		{&cConfigNew, "pythago_config_new"}, {&cConfigFree, "pythago_config_free"},
		{&cConfigSetHome, "pythago_config_set_home"}, {&cConfigSetProgramName, "pythago_config_set_program_name"},
		{&cConfigAppendSearchPath, "pythago_config_append_search_path"},
		{&cInitializeCore, "pythago_initialize_core"}, {&cInitializeMain, "pythago_initialize_main"},
		{&cConfigSetExecutable, "pythago_config_set_executable"}, {&cConfigSetArgv, "pythago_config_set_argv"},
		{&cRunMain, "Py_RunMain"}, {&cPyInstallModule, "pythago_py_install_module"},
		{&cConfigSetSignals, "pythago_config_set_signals"}, {&cPyFsInstall, "pythago_fs_install"},
		{&cPySetSpawn, "pythago_py_set_spawn"}, {&cPySetLimit, "pythago_py_set_limit"},
		{&cPyStarted, "pythago_py_started"}, {&cPyStopping, "pythago_py_stopping"},
		{&cPyDepth, "pythago_py_depth"}, {&cPyEnter, "pythago_py_enter"}, {&cPyLeave, "pythago_py_leave"},
		{&cPyRelease, "pythago_py_release"}, {&cPyReacquire, "pythago_py_reacquire"},
		{&cPyThreadMain, "pythago_py_thread_main"}, {&cPySetCallback, "pythago_py_set_callback"},
		{&cPyFastcall, "pythago_py_fastcall"}, {&cPyKind, "pythago_py_kind"},
		{&cPyGILDisabled, "pythago_py_gil_disabled"}, {&cPyStats, "pythago_py_stats"}, {&cPyBuild, "pythago_py_build"},
		{&cPyABI, "pythago_py_abi"}, {&cPyCall, "pythago_py_call"},
		{&cMemoryViewFromObject, "PyMemoryView_FromObject"},
		{&cGILStateGetThisThreadState, "PyGILState_GetThisThreadState"},

		{&cNone, "_Py_NoneStruct"}, {&cTrue, "_Py_TrueStruct"}, {&cFalse, "_Py_FalseStruct"},
		{&cExcRuntimeError, "PyExc_RuntimeError"},
	} {
		a, ok := lookupSymbol(syms, s.name)
		if !ok {
			return fmt.Errorf("py: %s is missing from pythago_symtab", s.name)
		}
		*s.p = a
	}
	if v := int32(call0(cPyABI)); v != glueABI {
		return fmt.Errorf("py: the native image's glue has ABI %d, but this package needs %d: regenerate the image with the matching pythago-dist", v, glueABI)
	}
	cExcRuntimeError = *(*uintptr)(unsafe.Pointer(cExcRuntimeError)) // a PyObject* cell
	initSingletons()
	goBufReleased = (*atomic.Int64)(unsafe.Pointer(call0(cGoBufReleased)))
	symbols = syms
	fromDouble, _ := lookupSymbol(syms, "PyFloat_FromDouble")
	asDouble, _ := lookupSymbol(syms, "PyFloat_AsDouble")
	purego.RegisterFunc(&floatFromDouble, fromDouble)
	purego.RegisterFunc(&floatAsDouble, asDouble)
	return nil
}

// lookupSymbol finds name in syms, else among the registered exports: on Windows the C
// API is looked up in python3XY.dll by name rather than listed (RegisterExports with no
// names).
func lookupSymbol(syms map[string]uintptr, name string) (uintptr, bool) {
	if a, ok := syms[name]; ok {
		return a, true
	}
	if x := exports.Load(); x != nil {
		return x.lookup(name)
	}
	return 0, false
}

// symbol returns the address of name in the resolved native image (0 if absent).
func symbol(name string) uintptr {
	a, _ := lookupSymbol(symbols, name)
	return a
}

// cString returns a NUL-terminated copy of s, on the heap. Callers keep it alive across
// the call.
//
// It must not be inlined: inlined into a caller that hands the pointer to C only as a
// uintptr (callStr0/callStr1), the buffer does not escape, and Go (1.25+) puts buffers
// of up to 32 bytes on the goroutine stack. Any copy of that stack between taking the
// uintptr and C's last read of it (morestack in a Go call between them, such as an
// append, or a Go callback during the C call) frees the old stack to the P's stack
// cache; the next goroutine stack allocated reuses it, and C reads that goroutine's
// frames.
//
//go:noinline
func cString(s string) *byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return &b[0]
}

func goString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

func incRef(p uintptr) { call1(cIncRef, p) }
func decRef(p uintptr) { call1(cDecRef, p) }
