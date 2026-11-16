package asgi

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/moriyoshi/pythago/internal/arena"
	"github.com/moriyoshi/pythago/internal/httpfield"
)

// Response header and trailer fields from the application are checked strictly. Malformed
// or misplaced fields are a long-standing source of response splitting, request smuggling
// and cache poisoning, so they are rejected (send() raises), never repaired or dropped
// silently.

// Trailer limits, per response.
const (
	maxTrailerFields = 64
	maxTrailerBytes  = 32 << 10
)

// forbiddenTrailers are fields that must not appear in trailers (RFC 9110 §6.5.1): a
// recipient needs them before the content, and an intermediary that merges trailers into
// the header section would let them override the real ones. The set covers message
// framing, routing, authentication, request modifiers, response controls, content
// metadata and state management (Set-Cookie, which Go's own list does not include).
var forbiddenTrailers = func() map[string]bool {
	m := map[string]bool{}
	for _, name := range []string{
		// framing and connection management
		"content-length", "transfer-encoding", "trailer", "te", "connection", "keep-alive",
		"proxy-connection", "upgrade",
		// routing
		"host",
		// authentication
		"authorization", "proxy-authorization", "www-authenticate", "proxy-authenticate", "realm",
		// request modifiers and controls
		"expect", "max-forwards", "range", "if-match", "if-none-match", "if-modified-since",
		"if-unmodified-since", "if-range", "cache-control", "pragma",
		// response controls
		"age", "date", "expires", "location", "retry-after", "vary", "warning",
		// content format and metadata
		"content-type", "content-encoding", "content-range", "content-language",
		"content-location", "content-disposition",
		// state management
		"set-cookie", "cookie",
	} {
		m[name] = true
	}
	return m
}()

// parseFields splits Python's packed "name\0value\0..." fields and checks them against
// count, the number of pairs Python packed. A NUL inside a name or value would shift the
// pairs (and could inject a field); it changes the field count, so a mismatch is an error.
// The pairs are taken from lane (a request's, if not nil: no limit on their number); the
// strings share one copy of packed.
func parseFields(lane *arena.SharedLane[[2]string], packed []byte, count int64, what string) ([][2]string, error) {
	if count == 0 {
		if len(packed) != 0 {
			return nil, fmt.Errorf("ASGI %s do not match their count", what)
		}
		return nil, nil
	}
	if count < 0 || count > 1<<16 {
		return nil, fmt.Errorf("invalid number of ASGI %s", what)
	}
	if int64(bytes.Count(packed, []byte{0})) != 2*count-1 {
		return nil, fmt.Errorf("ASGI %s must be (name, value) pairs of bytes without NUL", what)
	}
	var pairs [][2]string // full: an append (pathsend's content-length) copies
	if lane != nil {
		pairs = lane.Take(int(count))
	} else {
		pairs = make([][2]string, count)
	}
	rest := string(packed)
	for i := range pairs {
		name, rest0, _ := strings.Cut(rest, "\x00")
		value, rest1, _ := strings.Cut(rest0, "\x00")
		rest = rest1
		if !httpfield.ValidName(name) {
			return nil, fmt.Errorf("invalid ASGI %s name %q", what, name)
		}
		if !httpfield.ValidValue(value) {
			return nil, fmt.Errorf("invalid ASGI %s value for %q (control characters)", what, name)
		}
		pairs[i] = [2]string{name, value}
	}
	return pairs, nil
}

// checkResponseHeaders rejects framing that net/http would pass through ambiguously:
// more than one Content-Length, or one that is not a non-negative decimal.
func checkResponseHeaders(pairs [][2]string, trailers bool) error {
	cl := 0
	for _, kv := range pairs {
		if !strings.EqualFold(kv[0], "content-length") {
			continue
		}
		cl++
		v := kv[1]
		if v == "" || len(v) > 19 || strings.TrimLeft(v, "0123456789") != "" {
			return fmt.Errorf("invalid content-length %q", v)
		}
	}
	if cl > 1 {
		return errors.New("more than one content-length")
	}
	if cl > 0 && trailers {
		return errors.New("a response with trailers cannot have a content-length (trailers need chunked encoding)")
	}
	return nil
}

// checkTrailers rejects forbidden trailer fields and enforces the limits; fields and
// size count what the response already sent.
func checkTrailers(pairs [][2]string, fields, size *int) error {
	for _, kv := range pairs {
		if forbiddenTrailers[strings.ToLower(kv[0])] {
			return fmt.Errorf("%q is not allowed in trailers", kv[0])
		}
		*fields++
		*size += len(kv[0]) + len(kv[1])
	}
	if *fields > maxTrailerFields || *size > maxTrailerBytes {
		return fmt.Errorf("trailers exceed %d fields or %d bytes", maxTrailerFields, maxTrailerBytes)
	}
	return nil
}

// offerTrailers reports whether this request's response may carry trailers: HTTP/1.1
// (chunked) or HTTP/2, a method with a response body, and a client that said it accepts
// them (TE: trailers, RFC 9110 §10.1.4).
func offerTrailers(r *http.Request) bool {
	if !r.ProtoAtLeast(1, 1) || r.Method == http.MethodHead {
		return false
	}
	for _, te := range r.Header.Values("Te") {
		for _, part := range strings.Split(te, ",") {
			name, _, _ := strings.Cut(part, ";")
			if strings.EqualFold(strings.TrimSpace(name), "trailers") {
				return true
			}
		}
	}
	return false
}
