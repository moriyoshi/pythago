package asgi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/moriyoshi/pythago/asgi"
)

const wsapp = `
import asyncio, pythago_asgi

ws_errors = []

async def accept_or_record(send, **kw):
    try:
        await send({"type": "websocket.accept", **kw})
        return True
    except Exception as exc:
        ws_errors.append(str(exc))
        await send({"type": "websocket.close"})  # not accepted: a 403
        return False

async def ws_routes(scope, receive, send):
    assert (await receive())["type"] == "websocket.connect"
    path, q = scope["path"], scope["query_string"].decode()
    if path == "/ws/echo":
        sub = "chat" if "chat" in scope["subprotocols"] else None
        await send({"type": "websocket.accept", "subprotocol": sub, "headers": [(b"x-ws", b"1")]})
        while True:
            msg = await receive()
            if msg["type"] == "websocket.disconnect":
                return
            if msg.get("text") == "close":
                await send({"type": "websocket.close", "code": 4000, "reason": "bye"})
                return
            if msg.get("text") is not None:
                await send({"type": "websocket.send", "text": "echo:" + msg["text"]})
            else:
                await send({"type": "websocket.send", "bytes": b"echo:" + msg["bytes"]})
    elif path == "/ws/gc-wait":
        from testapp import wait_unreferenced  # testapp imports this module first
        await send({"type": "websocket.accept"})
        await wait_unreferenced()
        await send({"type": "websocket.send", "text": "survived"})
        await send({"type": "websocket.close"})
    elif path == "/ws/deny":
        await send({"type": "websocket.close"})
    elif path == "/ws/denyresp":
        await send({"type": "websocket.http.response.start", "status": 401, "headers": [(b"content-type", b"text/plain")]})
        await send({"type": "websocket.http.response.body", "body": b"nope"})
    elif path == "/ws/noanswer":
        return
    elif path == "/ws/raise":
        raise ValueError("before accept")
    elif path == "/ws/raise-after":
        await send({"type": "websocket.accept"})
        raise ValueError("after accept")
    elif path == "/ws/badsub":
        await accept_or_record(send, subprotocol="not-offered")
    elif path == "/ws/badheader":
        await accept_or_record(send, headers=[(b"sec-websocket-accept", b"forged")])
    elif path == "/ws/badclose":
        await send({"type": "websocket.accept"})
        try:
            await send({"type": "websocket.close", "code": 1005})
        except Exception as exc:
            ws_errors.append(str(exc))
            await send({"type": "websocket.close", "code": 1000})
    elif path == "/ws/push":
        await send({"type": "websocket.accept"})
        for i in range(int(q)):
            await send({"type": "websocket.send", "bytes": b"%06d" % i + b"x" * 1018})
        await send({"type": "websocket.close"})
    elif path == "/ws/slowread":
        await send({"type": "websocket.accept"})
        await asyncio.sleep(0.3)  # the client floods meanwhile
        ws_errors.append("queued %d" % len(receive.__self__.inbox))  # bounded by the credit window
        n = 0
        while (await receive())["type"] == "websocket.receive":
            n += 1
        ws_errors.append("slowread %d" % n)
    elif path == "/ws/disconnect":
        await send({"type": "websocket.accept"})
        while True:
            msg = await receive()
            if msg["type"] == "websocket.disconnect":
                ws_errors.append("disconnect %d %s" % (msg["code"], msg["reason"]))
                break
        try:
            await send({"type": "websocket.send", "text": "too late"})
        except pythago_asgi.ClientDisconnected:
            ws_errors.append("send after disconnect raised ClientDisconnected")
`

func wsURL(ts *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + path
}

func dial(t *testing.T, ts *httptest.Server, path string, opts *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return websocket.Dial(ctx, wsURL(ts, path), opts)
}

func wsErrors(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, body := get(t, ts, "/ws-errors")
	return string(body)
}

func TestWebSocketEcho(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c, resp, err := dial(t, ts, "/ws/echo", &websocket.DialOptions{Subprotocols: []string{"other", "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	if c.Subprotocol() != "chat" || resp.Header.Get("X-Ws") != "1" {
		t.Errorf("subprotocol %q, x-ws %q", c.Subprotocol(), resp.Header.Get("X-Ws"))
	}
	ctx := context.Background()
	for _, m := range []struct {
		typ  websocket.MessageType
		data string
	}{{websocket.MessageText, "héllo"}, {websocket.MessageBinary, "\x00\x01\xff"}, {websocket.MessageText, strings.Repeat("z", 100000)}} {
		if err := c.Write(ctx, m.typ, []byte(m.data)); err != nil {
			t.Fatal(err)
		}
		typ, data, err := c.Read(ctx)
		if err != nil || typ != m.typ || string(data) != "echo:"+m.data {
			t.Fatalf("got %v %q %v", typ, truncate(data), err)
		}
	}
	c.Write(ctx, websocket.MessageText, []byte("close"))
	_, _, err = c.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 4000 || ce.Reason != "bye" {
		t.Errorf("close: %v", err)
	}
}

func truncate(b []byte) string {
	if len(b) > 40 {
		return string(b[:40]) + "..."
	}
	return string(b)
}

// TestWebSocketTaskKeptAlive: as TestTaskKeptAlive, for a WebSocket's task.
func TestWebSocketTaskKeptAlive(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c, _, err := dial(t, ts, "/ws/gc-wait", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageText || string(data) != "survived" {
		t.Errorf("a WebSocket awaiting a future only it holds, across gc.collect(): %v %q %v", typ, data, err)
	}
}

func TestWebSocketHandshakeAnswers(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for path, want := range map[string]int{"/ws/deny": 403, "/ws/denyresp": 401, "/ws/noanswer": 500,
		"/ws/raise": 500, "/ws/badsub": 403, "/ws/badheader": 403} {
		_, resp, err := dial(t, ts, path, nil)
		if err == nil || resp == nil || resp.StatusCode != want {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Errorf("%s: status %d, want %d (%v)", path, code, want, err)
		}
	}
	// An exception after accept closes with 1011.
	c, _, err := dial(t, ts, "/ws/raise-after", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Errorf("raise-after: %v", err)
	}
	// An invalid close code raises in send(); the application then closes normally.
	c, _, err = dial(t, ts, "/ws/badclose", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("badclose: %v", err)
	}
	errs := wsErrors(t, ts)
	for _, want := range []string{`subprotocol \"not-offered\" was not offered`, `\"sec-websocket-accept\" cannot be set`,
		"invalid WebSocket close code 1005"} {
		if !strings.Contains(errs, want) {
			t.Errorf("no error %q in %s", want, errs)
		}
	}
}

// Cross-site WebSocket hijacking: a browser origin other than the server's host is
// refused before the application sees the request, unless configured.
func TestWebSocketOrigin(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	h := http.Header{}
	h.Set("Origin", "http://evil.example")
	if _, resp, err := dial(t, ts, "/ws/echo", &websocket.DialOptions{HTTPHeader: h}); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Errorf("cross-origin accepted: %v %v", err, resp)
	}
	h.Set("Origin", ts.URL) // same origin
	c, _, err := dial(t, ts, "/ws/echo", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("same origin refused: %v", err)
	}
	c.CloseNow()

	trusted, err := asgi.New("testapp:app", asgi.Options{WebSocketOrigins: []string{"*.trusted.example"}})
	if err != nil {
		t.Fatal(err)
	}
	defer trusted.Close(context.Background())
	ts2 := httptest.NewServer(trusted)
	defer ts2.Close()
	h.Set("Origin", "https://app.trusted.example")
	c, _, err = dial(t, ts2, "/ws/echo", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("configured origin refused: %v", err)
	}
	c.CloseNow()
	h.Set("Origin", "https://trusted.example.evil.example")
	if _, _, err := dial(t, ts2, "/ws/echo", &websocket.DialOptions{HTTPHeader: h}); err == nil {
		t.Error("a look-alike origin was accepted")
	}
}

func TestWebSocketProtocolErrors(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()
	// Invalid UTF-8 in a text message closes with 1007.
	c, _, err := dial(t, ts, "/ws/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(ctx, websocket.MessageText, []byte{'o', 0xff, 'k'})
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusInvalidFramePayloadData {
		t.Errorf("invalid UTF-8: %v", err)
	}
	// A message over the limit closes with 1009.
	small, err := asgi.New("testapp:app", asgi.Options{WebSocketMaxMessage: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close(context.Background())
	ts2 := httptest.NewServer(small)
	defer ts2.Close()
	c, _, err = dial(t, ts2, "/ws/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(ctx, websocket.MessageBinary, make([]byte, 2048))
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Errorf("oversized message: %v", err)
	}
	// Malformed handshakes never reach the application: /ws/deny would answer 403, so a
	// 400 or 426 shows the request was stopped before it (the library would also reject
	// some of these at accept time, after the application saw them).
	for req, want := range map[string]string{
		"GET /ws/deny HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 12\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n":                       "426",
		"GET /ws/deny HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: short\r\n\r\n":                                          "400",
		"GET /ws/deny HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: c2hvcnQ=\r\n\r\n":                                       "400",
		"POST /ws/deny HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nContent-Length: 0\r\n\r\n": "400",
		"GET /ws/deny HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n":                                              "400",
	} {
		// "close" makes the server end the connection after its answer (the upgrade token
		// stays for the checks that need it).
		req = strings.Replace(req, "Connection: Upgrade\r\n", "Connection: Upgrade, close\r\n", 1)
		if !strings.Contains(req, "Connection:") {
			req = strings.Replace(req, "\r\n\r\n", "\r\nConnection: close\r\n\r\n", 1)
		}
		got := raw(t, ts, req)
		if !strings.HasPrefix(got, "HTTP/1.1 "+want+" ") {
			t.Errorf("%q: %q", strings.SplitN(req, "\r\n", 2)[0], truncate([]byte(got)))
		}
	}
}

// Server pushes faster than the client reads (sends wait for the client), and a client
// floods while the application is busy (the credit window holds the rest in the socket).
func TestWebSocketBackpressure(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()
	c, _, err := dial(t, ts, "/ws/push?2000", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		_, data, err := c.Read(ctx)
		if err != nil || !strings.HasPrefix(string(data), fmt.Sprintf("%06d", i)) {
			t.Fatalf("message %d: %q %v", i, truncate(data), err)
		}
		if i%500 == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	c.CloseNow()

	c, _, err = dial(t, ts, "/ws/slowread", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		if err := c.Write(ctx, websocket.MessageBinary, []byte("flood")); err != nil {
			t.Fatal(err)
		}
	}
	c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(wsErrors(t, ts), "slowread 1000") {
		if time.Now().After(deadline) {
			t.Fatalf("flooded messages: %s", wsErrors(t, ts))
		}
		time.Sleep(20 * time.Millisecond)
	}
	// While the application was busy, Python held at most the credit window (16).
	var queued int
	fmt.Sscanf(wsErrors(t, ts)[strings.Index(wsErrors(t, ts), "queued "):], "queued %d", &queued)
	if queued < 1 || queued > 16 {
		t.Errorf("%d messages queued in Python while the application was busy", queued)
	}
}

func TestWebSocketClientClose(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c, _, err := dial(t, ts, "/ws/disconnect", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close(4001, "client bye")
	deadline := time.Now().Add(10 * time.Second)
	for {
		errs := wsErrors(t, ts)
		if strings.Contains(errs, "disconnect 4001 client bye") && strings.Contains(errs, "raised ClientDisconnected") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("app saw: %s", errs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
