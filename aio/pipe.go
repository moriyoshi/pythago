package aio

import (
	"net"
	"os"
)

// Pipes and child processes for GoEventLoop (asyncio subprocesses, connect_read_pipe and
// connect_write_pipe). A pipe's descriptor becomes an *os.File on Go's netpoller and
// travels through the same aioConn as a TCP connection: a reader goroutine, a writer
// goroutine, the same backpressure and zero-copy writes.

// pipeConn is a pipe as a net.Conn. It has no addresses; CloseWrite (write_eof) closes it,
// which is end of stream for a pipe.
type pipeConn struct{ *os.File }

func (pipeConn) LocalAddr() net.Addr  { return nil }
func (pipeConn) RemoteAddr() net.Addr { return nil }
func (p pipeConn) CloseWrite() error  { return p.File.Close() }
func (p pipeConn) String() string     { return p.File.Name() }

// watchHangup reports (evEOF) when the reading end of the write pipe c is closed, as
// asyncio's own write pipe transport learns it (waitHangup, per platform).
func (ac *aioConn) watchHangup() {
	p, ok := ac.c.(pipeConn)
	if !ok {
		return
	}
	rc, err := p.SyscallConn()
	if err != nil {
		return
	}
	go func() {
		if waitHangup(ac, rc) && !ac.aborted.Load() {
			ac.loop.post(completion{token: ac.token.Load(), kind: evEOF})
		}
	}()
}
