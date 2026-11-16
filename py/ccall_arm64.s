//go:build !windows

#include "textflag.h"

// pythago_cthunk runs a C call described by a cframe. runtime.cgocall enters it on the
// system stack (via asmcgocall) with R0 = the frame, under the C calling convention.
//
// The frame is on the calling goroutine's stack, which a Go callback made during the C
// call can grow and move. The result is therefore stored relative to the goroutine's
// stack top as it is after the call, the way cgo's generated wrappers use _cgo_topofstack.
TEXT pythago_cthunk<>(SB),NOSPLIT|NOFRAME,$0
	SUB	$48, RSP
	MOVD	R30, 0(RSP)
	MOVD	R19, 8(RSP)
	MOVD	R20, 16(RSP)
	MOVD	R21, 24(RSP)
	MOVD	R0, R19
	BL	_cgo_topofstack(SB)
	MOVD	R0, R20
	MOVD	0(R19), R9
	MOVD	8(R19), R0
	MOVD	16(R19), R1
	MOVD	24(R19), R2
	MOVD	32(R19), R3
	MOVD	40(R19), R4
	MOVD	48(R19), R5
	MOVD	56(R19), R6
	MOVD	64(R19), R7
	BL	(R9)
	MOVD	R0, R21
	BL	_cgo_topofstack(SB)
	SUB	R20, R0, R0
	ADD	R0, R19, R19
	MOVD	R21, 72(R19)
	MOVD	24(RSP), R21
	MOVD	16(RSP), R20
	MOVD	8(RSP), R19
	MOVD	0(RSP), R30
	ADD	$48, RSP
	RET

GLOBL ·cthunk(SB), NOPTR|RODATA, $8
DATA ·cthunk(SB)/8, $pythago_cthunk<>(SB)

// crosscall2 is the C-callable entry into Go that cgo-exported functions use (here,
// purego's fakecgo provides it).
GLOBL ·crosscall2(SB), NOPTR|RODATA, $8
DATA ·crosscall2(SB)/8, $crosscall2(SB)
