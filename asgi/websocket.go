package asgi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/moriyoshi/pythago/py"
)

// WebSockets (ASGI "websocket" scope). The RFC 6455 protocol (framing, masking,
// fragmentation, control frames, the closing handshake) is github.com/coder/websocket's;
// this file decides what reaches the application and what may be sent:
//
//   - The handshake is checked before the application sees the request: GET over HTTP/1.1,
//     Upgrade and Connection tokens, version 13, a 16-byte key, and the origin (same origin
//     by default; Options.WebSocketOrigins adds patterns; Options.WebSocketAnyOrigin hands
//     the check to the application). Cross-site WebSocket hijacking is the reason for the
//     default; uvicorn does not check.
//   - Nothing is sent until the application answers websocket.connect: accept completes
//     the upgrade, close before accept is a 403, and the websocket.http.response extension
//     sends the application's own denial.
//   - The accepted subprotocol must be one the client offered; extra accept headers are
//     validated like response headers and may not touch the upgrade's own fields.
//   - Messages are limited (Options.WebSocketMaxMessage, 16 MiB by default, as uvicorn);
//     permessage-deflate is off (no decompression bombs); text must be valid UTF-8 (the
//     library does not check: invalid text closes with 1007).
//   - Close codes must be ones an endpoint may send, reasons at most 123 bytes of UTF-8.
//   - Incoming messages use a credit window, so a fast client cannot queue unbounded data
//     in Python; sends wait for the client past the high-water mark.

const wsCredits = 16 // messages read ahead of the application

// isWebSocket reports whether r asks for a WebSocket: an HTTP/1.1 upgrade, or an
// HTTP/2 extended CONNECT (websocket_h2.go).
func isWebSocket(r *http.Request) bool {
	return headerHasToken(r.Header, "Upgrade", "websocket") || isWebSocketH2(r)
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// checkHandshake validates the upgrade request (RFC 6455 §4.2.1) and its origin.
func (s *Server) checkHandshake(r *http.Request) (status int, msg string) {
	if isWebSocketH2(r) { // RFC 8441 §4-5: CONNECT, :protocol websocket, version 13, no key
		if r.Header.Get("Sec-WebSocket-Version") != "13" {
			return http.StatusBadRequest, "unsupported WebSocket version"
		}
		if !s.originAllowed(r) {
			return http.StatusForbidden, "cross-origin WebSocket not allowed"
		}
		return 0, ""
	}
	switch {
	case r.Method != http.MethodGet || !r.ProtoAtLeast(1, 1) || r.ProtoMajor != 1:
		return http.StatusBadRequest, "WebSocket upgrades need GET over HTTP/1.1"
	case !headerHasToken(r.Header, "Connection", "upgrade"):
		return http.StatusBadRequest, "missing Connection: upgrade"
	case r.Header.Get("Sec-WebSocket-Version") != "13":
		return http.StatusUpgradeRequired, "unsupported WebSocket version"
	}
	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 {
		return http.StatusBadRequest, "invalid Sec-WebSocket-Key"
	}
	if !s.originAllowed(r) {
		return http.StatusForbidden, "cross-origin WebSocket not allowed"
	}
	return 0, ""
}

// originAllowed: no Origin (not a browser), the request's own host, or a configured
// pattern (path.Match against the origin host, or scheme://host if the pattern has "://",
// as coder/websocket matches them).
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || s.opts.WebSocketAnyOrigin {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, p := range s.opts.WebSocketOrigins {
		target := u.Host
		if strings.Contains(p, "://") {
			target = u.Scheme + "://" + u.Host
		}
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(target)); ok {
			return true
		}
	}
	return false
}

// subprotocols lists the client's Sec-WebSocket-Protocol offers.
func subprotocols(r *http.Request) []string {
	var out []string
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// wsAcceptForbidden are fields the upgrade itself sets; the application may not.
func wsAcceptForbidden(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "sec-websocket-") || n == "upgrade" || n == "connection" ||
		n == "content-length" || n == "transfer-encoding" || n == "te" || n == "trailer"
}

// validCloseCode reports whether an endpoint may send code in a close frame (RFC 6455
// §7.4; 1004-1006 and 1015 are reserved for local use, 1016-2999 for future ones).
func validCloseCode(code int64) bool {
	switch {
	case code >= 1000 && code <= 1003, code >= 1007 && code <= 1014:
		return true
	case code >= 3000 && code <= 4999:
		return true
	}
	return false
}

// serveWebSocket runs a WebSocket request: the application's answer to
// websocket.connect, then the connection.
func (s *Server) serveWebSocket(w http.ResponseWriter, r *http.Request, q *request, wk *worker) {
	var (
		conn       *websocket.Conn
		readerDone chan struct{}
		status     int // a denial response (websocket.http.response.start)
	)
	connCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := r.Context()
	for {
		select {
		case <-q.kick:
		case <-ctx.Done(): // before the upgrade: the client went away
			if conn == nil {
				wk.loop.Post(wk.token, py.Tuple{"wd", q.h, int64(1006), ""})
				return
			}
			ctx = context.Background() // hijacked: the reader watches the connection
			continue
		case <-readerDone: // the connection ended; the reader told Python
			return
		}
		q.mu.Lock()
		acts := q.queue
		q.queue = nil
		q.mu.Unlock()
		for i, a := range acts {
			switch a.kind {
			case actWSAccept:
				hdr := w.Header()
				for _, kv := range a.headers {
					hdr[http.CanonicalHeaderKey(kv[0])] = append(hdr[http.CanonicalHeaderKey(kv[0])], kv[1])
				}
				var subs []string
				if a.subprotocol != "" {
					subs = []string{a.subprotocol}
				}
				var c *websocket.Conn
				var err error
				if isWebSocketH2(r) {
					// RFC 8441: a 200 opens the stream for frames.
					if a.subprotocol != "" {
						hdr.Set("Sec-WebSocket-Protocol", a.subprotocol)
					}
					w.WriteHeader(http.StatusOK)
					if err = http.NewResponseController(w).Flush(); err == nil {
						c, err = acceptStream(w, r, a.subprotocol)
					}
				} else {
					c, err = websocket.Accept(w, r, &websocket.AcceptOptions{
						Subprotocols:       subs,
						InsecureSkipVerify: true, // checkHandshake checked the origin already
						CompressionMode:    websocket.CompressionDisabled,
					})
				}
				if err != nil {
					releaseRest(wk, acts[i+1:])
					wk.loop.Post(wk.token, py.Tuple{"wd", q.h, int64(1006), ""})
					return
				}
				conn = c
				conn.SetReadLimit(s.opts.WebSocketMaxMessage)
				readerDone = make(chan struct{})
				go q.wsReader(connCtx, conn, readerDone)
			case actWSSend:
				if conn == nil { // not accepted (Python prevents this; Go does not rely on it)
					if a.obj != nil {
						wk.release(a.obj)
					}
					continue
				}
				err := conn.Write(connCtx, a.msgType, a.data)
				if a.obj != nil {
					wk.release(a.obj)
				}
				q.written(len(a.data))
				_ = err // a failed write ends the connection; the reader reports it
			case actWSClose:
				releaseRest(wk, acts[i+1:])
				if conn == nil { // close before accept: the handshake is refused
					w.WriteHeader(http.StatusForbidden)
					return
				}
				conn.Close(websocket.StatusCode(a.code), a.reason)
				return
			case actStart: // the websocket.http.response extension
				for _, kv := range a.headers {
					w.Header()[http.CanonicalHeaderKey(kv[0])] = append(w.Header()[http.CanonicalHeaderKey(kv[0])], kv[1])
				}
				status = a.status
			case actBody:
				if status != 0 {
					w.WriteHeader(status)
					status = 0
				}
				if len(a.data) > 0 {
					w.Write(a.data)
				}
				if a.obj != nil {
					wk.release(a.obj)
				}
				q.written(len(a.data))
				if !a.more {
					releaseRest(wk, acts[i+1:])
					return
				}
			case actAbort:
				releaseRest(wk, acts[i+1:])
				if conn != nil {
					conn.Close(websocket.StatusInternalError, "")
					return
				}
				panic(http.ErrAbortHandler)
			}
		}
	}
}

// wsReader passes messages to the application as credits allow, and reports the end of
// the connection: ("wm", h, is_text, data), then ("wd", h, code, reason).
func (q *request) wsReader(ctx context.Context, conn *websocket.Conn, done chan struct{}) {
	defer close(done)
	wk := q.wk
	for {
		select {
		case <-q.credit:
		case <-ctx.Done():
			return
		}
		typ, data, err := conn.Read(ctx)
		if err != nil {
			code, reason := int64(1006), "" // abnormal closure, unless the peer said otherwise
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				code, reason = int64(ce.Code), ce.Reason
			}
			if !utf8.ValidString(reason) {
				reason = ""
			}
			wk.loop.Post(wk.token, py.Tuple{"wd", q.h, code, reason})
			return
		}
		if typ == websocket.MessageText && !utf8.Valid(data) {
			conn.Close(websocket.StatusInvalidFramePayloadData, "invalid UTF-8")
			wk.loop.Post(wk.token, py.Tuple{"wd", q.h, int64(websocket.StatusInvalidFramePayloadData), ""})
			return
		}
		wk.loop.Post(wk.token, py.Tuple{"wm", q.h, typ == websocket.MessageText, data})
	}
}

// ---- _pythago_asgi functions for WebSockets ----

func wsAcceptAction(q *request, sub, packed, count *py.Object) (action, error) {
	s, err := sub.GoString()
	if err != nil {
		return action{}, err
	}
	if s != "" {
		ok := false
		for _, p := range q.subprotocols {
			if p == s {
				ok = true
			}
		}
		if !ok {
			return action{}, fmt.Errorf("subprotocol %q was not offered by the client", s)
		}
	}
	pairs, err := packedFields(packed, count, "headers")
	if err != nil {
		return action{}, err
	}
	for _, kv := range pairs {
		if wsAcceptForbidden(kv[0]) {
			return action{}, fmt.Errorf("%q cannot be set on a WebSocket accept", kv[0])
		}
	}
	return action{kind: actWSAccept, subprotocol: s, headers: pairs}, nil
}

func wsSendAction(isText, data *py.Object) (action, error) {
	t, err := isText.Truth()
	if err != nil {
		return action{}, err
	}
	act := action{kind: actWSSend, msgType: websocket.MessageBinary}
	if t {
		act.msgType = websocket.MessageText
	}
	b, err := data.UnsafeBytes()
	if err != nil {
		return action{}, err
	}
	if t && !utf8.Valid(b) {
		return action{}, errors.New("WebSocket text must be valid UTF-8")
	}
	act.data = b
	if len(b) > 0 {
		act.obj = data.NewRef()
	}
	return act, nil
}

func wsCloseAction(code, reason *py.Object) (action, error) {
	c, err := code.Int64()
	if err != nil {
		return action{}, err
	}
	if !validCloseCode(c) {
		return action{}, fmt.Errorf("invalid WebSocket close code %d", c)
	}
	r, err := reason.GoString()
	if err != nil {
		return action{}, err
	}
	if len(r) > 123 {
		return action{}, errors.New("WebSocket close reasons are at most 123 bytes")
	}
	return action{kind: actWSClose, code: int(c), reason: r}, nil
}
