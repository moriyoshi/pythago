package wsgi

import (
	"bytes"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// environ packs the request's CGI-style environ strings as "name\0value\0...", which
// Python decodes as latin-1 (PEP 3333's "bytes as str": each byte one code point, so a
// UTF-8 path or header arrives as the application expects to re-encode it). It reports
// false for a NUL in any value, which the packing cannot carry: it would shift the pairs
// and let the client set environ keys. A header cannot carry one (net/http rejects it;
// this does not rely on that), but the decoded path can ("/a%00b").
//
// Header names become HTTP_NAME. Names containing "_" are dropped: they would be
// indistinguishable from their "-" spelling (a proxy that strips X-Forwarded-For but not
// X_Forwarded_For would let a client set HTTP_X_FORWARDED_FOR), as nginx and gunicorn
// do (wsgiref passes them on). So is Proxy, which CGI-derived code reads as HTTP_PROXY (httpoxy).
// Repeated headers are joined with ", ", Cookie with "; " (HTTP/2 splits cookies).
// Request trailers are never passed on.
//
// The environ is appended to dst (the request's memory).
//
// HTTP_HOST is the request's host as net/http routes it (r.Host): HTTP/1.1 moves the
// Host header there, but HTTP/2 leaves a host field in the headers next to :authority,
// and one that differs from it makes the request malformed (RFC 9113 §8.3.1), so false
// is reported for it rather than letting the application see another host than Go did.
// CONTENT_LENGTH is the body length net/http enforces, not the header as sent (an
// HTTP/2 "05" or "+5").
func environ(dst []byte, r *http.Request, pathInfo string) ([]byte, bool) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	serverName, serverPort := r.Host, ""
	if h, p, err := net.SplitHostPort(r.Host); err == nil {
		serverName, serverPort = h, p
	}
	if serverName == "" { // HTTP/1.0 without Host: the address the request came in on
		if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			serverName, serverPort, _ = net.SplitHostPort(a.String())
		}
	}
	if serverPort == "" {
		serverPort = "80"
		if r.TLS != nil {
			serverPort = "443"
		}
	}
	remoteAddr, remotePort, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteAddr, remotePort = r.RemoteAddr, ""
	}

	b := dst[:0]
	ok := true
	add := func(k, v string) {
		if strings.IndexByte(v, 0) >= 0 || strings.IndexByte(k, 0) >= 0 {
			ok = false
		}
		if len(b) > 0 {
			b = append(b, 0)
		}
		b = append(append(append(b, k...), 0), v...)
	}
	// addName is add for a name built in a buffer (no string copy of it).
	addName := func(k []byte, v string) {
		if strings.IndexByte(v, 0) >= 0 || bytes.IndexByte(k, 0) >= 0 {
			ok = false
		}
		if len(b) > 0 {
			b = append(b, 0)
		}
		b = append(append(append(b, k...), 0), v...)
	}
	add("REQUEST_METHOD", r.Method)
	add("PATH_INFO", pathInfo)
	add("QUERY_STRING", r.URL.RawQuery)
	add("REQUEST_URI", r.RequestURI)
	add("SERVER_NAME", serverName)
	add("SERVER_PORT", serverPort)
	add("SERVER_PROTOCOL", r.Proto)
	add("REMOTE_ADDR", remoteAddr)
	add("REMOTE_PORT", remotePort)
	add("wsgi.url_scheme", scheme)
	if r.Host != "" {
		add("HTTP_HOST", r.Host)
	}
	if !sameHost(r) {
		return nil, false
	}
	var nameBuf [64]byte // most header names fit: their HTTP_ names are built here
	name := nameBuf[:0]
	for k, vs := range r.Header {
		if strings.IndexByte(k, '_') >= 0 || len(vs) == 0 {
			continue
		}
		switch k {
		case "Content-Type":
			name = append(name[:0], "CONTENT_TYPE"...)
		case "Content-Length":
			if r.ContentLength >= 0 {
				add("CONTENT_LENGTH", strconv.FormatInt(r.ContentLength, 10))
			}
			continue
		case "Proxy", "Host":
			continue
		default:
			name = append(name[:0], "HTTP_"...)
			for i := 0; i < len(k); i++ {
				c := k[i]
				switch {
				case 'a' <= c && c <= 'z':
					c -= 'a' - 'A'
				case c == '-':
					c = '_'
				}
				name = append(name, c)
			}
		}
		sep := ", "
		if k == "Cookie" {
			sep = "; "
		}
		addName(name, strings.Join(vs, sep))
	}
	return b, ok
}

// sameHost reports whether every host header field the request still carries (HTTP/2)
// names r.Host.
func sameHost(r *http.Request) bool {
	for _, h := range r.Header.Values("Host") {
		if h != r.Host {
			return false
		}
	}
	return true
}
