//go:build !windows

#include "textflag.h"

// pythago_cthunk runs a C call described by a cframe. runtime.cgocall enters it on the
// system stack (via asmcgocall) with DI = the frame, under the C calling convention.
//
// The frame is on the calling goroutine's stack, which a Go callback made during the C
// call can grow and move. The result is therefore stored relative to the goroutine's
// stack top as it is after the call, the way cgo's generated wrappers use _cgo_topofstack.
TEXT pythago_cthunk<>(SB),NOSPLIT|NOFRAME,$0
	// Three pushes on top of the return address, and the two stack argument words,
	// keep SP 16-byte aligned for the calls.
	PUSHQ	BX
	PUSHQ	R12
	PUSHQ	R13
	SUBQ	$16, SP
	MOVQ	DI, BX
	CALL	_cgo_topofstack(SB)
	MOVQ	AX, R12
	MOVQ	0(BX), R11
	MOVQ	8(BX), DI
	MOVQ	16(BX), SI
	MOVQ	24(BX), DX
	MOVQ	32(BX), CX
	MOVQ	40(BX), R8
	MOVQ	48(BX), R9
	MOVQ	56(BX), R10
	MOVQ	R10, 0(SP)
	MOVQ	64(BX), R10
	MOVQ	R10, 8(SP)
	XORL	AX, AX // no vector arguments
	CALL	R11
	MOVQ	AX, R13
	CALL	_cgo_topofstack(SB)
	SUBQ	R12, AX
	ADDQ	AX, BX
	MOVQ	R13, 72(BX)
	ADDQ	$16, SP
	POPQ	R13
	POPQ	R12
	POPQ	BX
	RET

GLOBL ·cthunk(SB), NOPTR|RODATA, $8
DATA ·cthunk(SB)/8, $pythago_cthunk<>(SB)

// crosscall2 is the C-callable entry into Go that cgo-exported functions use (here,
// purego's fakecgo provides it).
GLOBL ·crosscall2(SB), NOPTR|RODATA, $8
DATA ·crosscall2(SB)/8, $crosscall2(SB)
