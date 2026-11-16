package asgi_test

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// A minimal client-side RFC 6455 codec: masked frames out, unmasked frames in.
func wsWrite(w io.Writer, op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 65536:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	var key [4]byte
	rand.Read(key[:])
	hdr = append(hdr, key[:]...)
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ key[i%4]
	}
	_, err := w.Write(append(hdr, masked...))
	return err
}

func wsRead(r io.Reader) (op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return
	}
	if h[1]&0x80 != 0 {
		return 0, nil, io.ErrUnexpectedEOF // servers must not mask
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(r, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(r, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return h[0] & 0x0f, payload, err
}

// dialH2 opens an RFC 8441 WebSocket stream: extended CONNECT with :protocol websocket.
func dialH2(t *testing.T, ts *httptest.Server, path string, hdr map[string]string) (*http.Response, *io.PipeWriter, error) {
	t.Helper()
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodConnect, ts.URL+path, pr)
	req.Header.Set(":protocol", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := h2Client(ts).Do(req)
	return resp, pw, err
}

// h2Client speaks HTTP/2 through golang.org/x/net/http2, whose transport sends :protocol
// (net/http's Transport rejects it as a header name before reaching HTTP/2).
func h2Client(ts *httptest.Server) *http.Client {
	tlsConf := ts.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tlsConf.NextProtos = []string{"h2"}
	return &http.Client{Transport: &http2.Transport{TLSClientConfig: tlsConf}}
}

func h2Server(t *testing.T) *httptest.Server {
	ts := httptest.NewUnstartedServer(srv)
	ts.EnableHTTP2 = true
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

func TestWebSocketHTTP2(t *testing.T) {
	ts := h2Server(t)
	resp, pw, err := dialH2(t, ts, "/ws/echo", map[string]string{"Sec-WebSocket-Protocol": "other, chat"})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != 200 || resp.Header.Get("Sec-WebSocket-Protocol") != "chat" ||
		resp.Header.Get("X-Ws") != "1" {
		t.Fatalf("%s %d %v", resp.Proto, resp.StatusCode, resp.Header)
	}
	for _, m := range []struct {
		op   byte
		data string
	}{{1, "héllo over h2"}, {2, "\x00\xff"}, {1, strings.Repeat("y", 70000)}} {
		if err := wsWrite(pw, m.op, []byte(m.data)); err != nil {
			t.Fatal(err)
		}
		op, data, err := wsRead(resp.Body)
		if err != nil || op != m.op || string(data) != "echo:"+m.data {
			t.Fatalf("got op %d %q %v", op, truncate(data), err)
		}
	}
	wsWrite(pw, 1, []byte("close"))
	op, data, err := wsRead(resp.Body)
	if err != nil || op != 8 || binary.BigEndian.Uint16(data) != 4000 || string(data[2:]) != "bye" {
		t.Errorf("close frame: op %d %q %v", op, data, err)
	}
	wsWrite(pw, 8, data[:2]) // echo the close
	pw.Close()

	// Invalid UTF-8 closes with 1007 here too.
	resp, pw, err = dialH2(t, ts, "/ws/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	wsWrite(pw, 1, []byte{0xc3, 0x28})
	op, data, err = wsRead(resp.Body)
	if err != nil || op != 8 || binary.BigEndian.Uint16(data) != 1007 {
		t.Errorf("invalid UTF-8: op %d %q %v", op, data, err)
	}
	pw.Close()
	resp.Body.Close()
}

func TestWebSocketHTTP2Refusals(t *testing.T) {
	ts := h2Server(t)
	// Cross-site origin, wrong version, and a denial from the application.
	for _, c := range []struct {
		path string
		hdr  map[string]string
		want int
	}{
		{"/ws/echo", map[string]string{"Origin": "https://evil.example"}, 403},
		{"/ws/echo", map[string]string{"Sec-WebSocket-Version": "8"}, 400},
		{"/ws/deny", nil, 403},
		{"/ws/denyresp", nil, 401},
	} {
		resp, pw, err := dialH2(t, ts, c.path, c.hdr)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if resp.StatusCode != c.want {
			t.Errorf("%s %v: %d, want %d", c.path, c.hdr, resp.StatusCode, c.want)
		}
		pw.Close()
		resp.Body.Close()
	}
	// Other extended CONNECT protocols are not passed to the application.
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodConnect, ts.URL+"/ws/echo", pr)
	req.Header.Set(":protocol", "connect-udp")
	if resp, err := h2Client(ts).Do(req); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("connect-udp: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	pw.Close()
	// Nor is a plain CONNECT (HTTP/1.1 here).
	plain := httptest.NewServer(srv)
	defer plain.Close()
	got := raw(t, plain, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nConnection: close\r\n\r\n")
	if !strings.HasPrefix(got, "HTTP/1.1 405 ") {
		t.Errorf("CONNECT: %q", truncate([]byte(got)))
	}
	_ = time.Second
}
