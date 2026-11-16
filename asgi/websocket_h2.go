package asgi

import (
	"bufio"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// WebSockets over HTTP/2 (RFC 8441): the client sends an extended CONNECT with
// :protocol websocket on a stream, the server answers 200, and the stream then carries
// ordinary RFC 6455 frames (masking included). There is no Sec-WebSocket-Key exchange.
//
// Go's HTTP/2 server only accepts extended CONNECT when the process runs with
// GODEBUG=http2xconnect=1 (read from the environment at start-up; Go issue #71128 keeps it
// off by default because browsers then try WebSockets over HTTP/2 against every handler
// of the server). Without it, browsers keep using HTTP/1.1 upgrades.
//
// coder/websocket only accepts hijacked HTTP/1.1 connections, so after answering the
// CONNECT ourselves we hand it the stream through a stand-in ResponseWriter whose Hijack
// returns the stream as a net.Conn; its own 101 response goes nowhere. The framing, and
// every check in websocket.go, is the same as over HTTP/1.1.

// isWebSocketH2 reports whether r is an RFC 8441 WebSocket request.
func isWebSocketH2(r *http.Request) bool {
	return r.ProtoMajor == 2 && r.Method == http.MethodConnect && r.Header.Get(":protocol") == "websocket"
}

// acceptStream completes the WebSocket on an extended CONNECT stream whose 200 response
// head was already written: it gives coder/websocket the stream as a connection.
func acceptStream(w http.ResponseWriter, r *http.Request, subprotocol string) (*websocket.Conn, error) {
	sc := &streamConn{w: w, r: r, rc: http.NewResponseController(w), closed: make(chan struct{})}
	// A request shaped like the HTTP/1.1 upgrade coder/websocket checks; the real
	// request was checked by checkHandshake.
	fake, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "/", nil)
	if err != nil {
		return nil, err
	}
	fake.Header.Set("Connection", "Upgrade")
	fake.Header.Set("Upgrade", "websocket")
	fake.Header.Set("Sec-WebSocket-Version", "13")
	fake.Header.Set("Sec-WebSocket-Key", "AAAAAAAAAAAAAAAAAAAAAA==")
	var subs []string
	if subprotocol != "" {
		fake.Header.Set("Sec-WebSocket-Protocol", subprotocol)
		subs = []string{subprotocol}
	}
	return websocket.Accept(&streamWriter{conn: sc}, fake, &websocket.AcceptOptions{
		Subprotocols:       subs,
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
}

// streamWriter is the stand-in ResponseWriter: coder/websocket's handshake headers and
// 101 status are dropped (the stream already has its 200), and Hijack yields the stream.
type streamWriter struct {
	conn   *streamConn
	header http.Header
}

func (sw *streamWriter) Header() http.Header {
	if sw.header == nil {
		sw.header = http.Header{}
	}
	return sw.header
}
func (sw *streamWriter) Write(b []byte) (int, error) { return len(b), nil }
func (sw *streamWriter) WriteHeader(int)             {}
func (sw *streamWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return sw.conn, bufio.NewReadWriter(bufio.NewReader(sw.conn), bufio.NewWriter(sw.conn)), nil
}

// streamConn is an HTTP/2 stream as a net.Conn: reads come from the request body,
// writes go to the response and are flushed (each is a whole frame batch from the
// WebSocket writer). Close ends both directions; the handler then returns, which ends
// the stream.
type streamConn struct {
	w      http.ResponseWriter
	r      *http.Request
	rc     *http.ResponseController
	mu     sync.Mutex // serializes writes and Close
	once   sync.Once
	closed chan struct{}
}

func (c *streamConn) Read(b []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	return c.r.Body.Read(b)
}

func (c *streamConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	n, err := c.w.Write(b)
	if err == nil {
		err = c.rc.Flush()
	}
	return n, err
}

func (c *streamConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		c.r.Body.Close()
	})
	return nil
}

func (c *streamConn) LocalAddr() net.Addr {
	if a, ok := c.r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		return a
	}
	return dummyAddr("local")
}
func (c *streamConn) RemoteAddr() net.Addr { return dummyAddr(c.r.RemoteAddr) }
func (c *streamConn) SetDeadline(t time.Time) error {
	c.rc.SetReadDeadline(t)
	return c.rc.SetWriteDeadline(t)
}
func (c *streamConn) SetReadDeadline(t time.Time) error  { return c.rc.SetReadDeadline(t) }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return c.rc.SetWriteDeadline(t) }

type dummyAddr string

func (a dummyAddr) Network() string { return "h2-stream" }
func (a dummyAddr) String() string  { return string(a) }

var (
	_ net.Conn      = (*streamConn)(nil)
	_ http.Hijacker = (*streamWriter)(nil)
)
