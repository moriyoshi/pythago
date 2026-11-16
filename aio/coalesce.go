package aio

import (
	"crypto/tls"
	"errors"
	"net"
	"syscall"
)

// Coalesced reads: after a read that returned data, the reader goroutines keep reading
// what is already readable, without waiting, so one event carries it all. Without this a
// Protocol (StreamReader) got one EV_DATA per read, and a TLS read returns one record
// (at most 16 KiB): bulk transfers ran at a third of asyncio's own loop.

// errWouldBlock is a read that found nothing ready. It is a temporary timeout, which
// crypto/tls does not make sticky: the connection's state, a partial record included,
// stays as it was.
var errWouldBlock error = wouldBlock{}

type wouldBlock struct{}

func (wouldBlock) Error() string   { return "goaio: no data ready" }
func (wouldBlock) Timeout() bool   { return true }
func (wouldBlock) Temporary() bool { return true }

// nbConn is the socket under a TLS connection. While nonblock is set (only by the reader
// goroutine, the connection's only reader, around a coalescing read), Read does not wait.
// A read deadline in the past would not do: the poller then fails the read without
// trying the socket, data or not.
type nbConn struct {
	net.Conn
	rc       syscall.RawConn
	nonblock bool
}

func (c *nbConn) Read(p []byte) (int, error) {
	if !c.nonblock {
		return c.Conn.Read(p)
	}
	return readNow(c.rc, p)
}

// newNBConn wraps c for a TLS connection, if it has a descriptor.
func newNBConn(c net.Conn) net.Conn {
	if sc, ok := c.(syscall.Conn); ok {
		if rc, err := sc.SyscallConn(); err == nil {
			return &nbConn{Conn: c, rc: rc}
		}
	}
	return c
}

// coalesceMin is the smallest first read worth reading more after: one TLS record's
// plaintext. A shorter read means the stream is not busy (request/response), where the
// extra read would only find nothing, at a cost (3% fewer 1 KiB round trips on 64
// connections when every read tried).
const coalesceMin = 16 << 10

// readMore fills p with what is readable now, after a read that returned data. An end of
// stream or an error met on the way is returned with the data read before it.
func (ac *aioConn) readMore(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		var m int
		var err error
		switch {
		case ac.nb != nil: // TLS: records crypto/tls has buffered, then the socket's
			ac.nb.nonblock = true
			m, err = ac.c.Read(p[n:])
			ac.nb.nonblock = false
		case ac.rc != nil:
			m, err = readNow(ac.rc, p[n:])
		default:
			return n, nil
		}
		n += m
		if errors.Is(err, errWouldBlock) || err == nil && m == 0 {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// coalescing sets up readMore for c: its RawConn, or the nbConn under a TLS connection.
func (ac *aioConn) coalescing() {
	switch c := ac.c.(type) {
	case *tls.Conn:
		ac.nb, _ = c.NetConn().(*nbConn)
	case syscall.Conn:
		ac.rc, _ = c.SyscallConn()
	}
}
