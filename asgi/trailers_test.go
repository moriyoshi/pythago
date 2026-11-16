package asgi_test

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/internal/h2raw"
)

// getTE fetches path with TE: trailers and reads the body to the end (trailers arrive
// after it).
func getTE(t *testing.T, client *http.Client, url string, te bool) (*http.Response, string, error) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if te {
		req.Header.Set("TE", "trailers")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp, string(body), err
}

func TestTrailers(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, body, err := getTE(t, ts.Client(), ts.URL+"/t/ok", true)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-Offered") != "yes" || body != "part1 part2" ||
		resp.Trailer.Get("X-Checksum") != "abc" || resp.Trailer.Get("Grpc-Status") != "0" ||
		resp.Trailer.Get("X-Second") != "2" {
		t.Errorf("offered=%q body=%q trailer=%v", resp.Header.Get("X-Offered"), body, resp.Trailer)
	}
	if len(resp.TransferEncoding) == 0 || resp.TransferEncoding[0] != "chunked" {
		t.Errorf("transfer encoding %v, want chunked", resp.TransferEncoding)
	}

	resp, body, err = getTE(t, ts.Client(), ts.URL+"/t/single", true)
	if err != nil || body != "short" || resp.Trailer.Get("X-Checksum") != "single" || resp.ContentLength != -1 {
		t.Errorf("single body: %v %q trailer %v content-length %d", err, body, resp.Trailer, resp.ContentLength)
	}

	// Not offered without TE: trailers, with another TE, or for HEAD.
	if resp, body, _ := getTE(t, ts.Client(), ts.URL+"/t/ok", false); resp.Header.Get("X-Offered") != "no" || body != "no trailers" {
		t.Errorf("without TE: %q %q", resp.Header.Get("X-Offered"), body)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/t/ok", nil)
	req.Header.Set("TE", "gzip;q=0.5, deflate")
	if resp, err := ts.Client().Do(req); err != nil || resp.Header.Get("X-Offered") != "no" {
		t.Errorf("TE without trailers: %v %v", err, resp.Header)
	} else {
		resp.Body.Close()
	}
	req, _ = http.NewRequest("HEAD", ts.URL+"/t/ok", nil)
	req.Header.Set("TE", "trailers")
	if resp, err := ts.Client().Do(req); err != nil || resp.Header.Get("X-Offered") != "no" {
		t.Errorf("HEAD: %v %v", err, resp.Header)
	} else {
		resp.Body.Close()
	}
	// HTTP/1.0 has no chunked encoding, so no trailers.
	if got := raw(t, ts, "GET /t/ok HTTP/1.0\r\nHost: x\r\nTE: trailers\r\n\r\n"); !strings.Contains(got, "X-Offered: no") {
		t.Errorf("HTTP/1.0: %q", got)
	}
}

func TestTrailersHTTP2(t *testing.T) {
	ts := httptest.NewUnstartedServer(srv)
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()
	resp, body, err := getTE(t, ts.Client(), ts.URL+"/t/ok", true)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ProtoMajor != 2 || body != "part1 part2" || resp.Trailer.Get("Grpc-Status") != "0" ||
		resp.Trailer.Get("X-Second") != "2" {
		t.Errorf("proto %s body %q trailer %v", resp.Proto, body, resp.Trailer)
	}
}

// Invalid, forbidden or misplaced trailers make send() raise, and the response is not
// completed: the connection is aborted, so the client sees a truncated message rather
// than a response that looks complete without its trailers.
func TestTrailerRejections(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cases := []string{
		"/t/bad?content-length", "/t/bad?transfer-encoding", "/t/bad?Host", "/t/bad?set-cookie",
		"/t/bad?authorization", "/t/bad?trailer", "/t/bad?content-type", "/t/bad?location",
		"/t/bad?value", "/t/bad?name", "/t/bad?many", "/t/early", "/t/missing",
	}
	for _, path := range cases {
		resp, _, err := getTE(t, ts.Client(), ts.URL+path, true)
		if err == nil {
			t.Errorf("%s: completed normally (trailer %v)", path, resp.Trailer)
			continue
		}
		if resp != nil && (resp.Trailer.Get("Content-Length") != "" || resp.Trailer.Get("Set-Cookie") != "") {
			t.Errorf("%s: forbidden trailer delivered: %v", path, resp.Trailer)
		}
	}
	// trailers=True when not offered raises at start (the app falls back).
	if resp, body, err := getTE(t, ts.Client(), ts.URL+"/t/notoffered", false); err != nil || body != "fallback" {
		t.Errorf("notoffered: %v %q %v", err, body, resp)
	}
	// Content-Length with trailers is refused before anything is sent: 500.
	if resp, _, err := getTE(t, ts.Client(), ts.URL+"/t/clen", true); err != nil || resp.StatusCode != 500 {
		t.Errorf("clen: %v %v", err, resp)
	}
	_, errs, _ := getTE(t, ts.Client(), ts.URL+"/h/errors", false)
	for _, want := range []string{`"content-length\" is not allowed in trailers`, `"set-cookie\" is not allowed`,
		"control characters", "invalid ASGI trailers name", "exceed 64 fields", "before the final http.response.body",
		"were not offered"} {
		if !strings.Contains(errs, want) {
			t.Errorf("no error mentioning %q in %s", want, errs)
		}
	}
}

// Header injection through the application's response headers is refused (500), never
// passed through or repaired.
func TestHeaderValidation(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for _, path := range []string{"/h/inject", "/h/crlf", "/h/badname", "/h/status?101", "/h/status?99",
		"/h/status?600", "/h/status?1000", "/h/dupcl"} {
		got := raw(t, ts, "GET "+path+" HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		if !strings.HasPrefix(got, "HTTP/1.1 500 ") || strings.Contains(strings.ToLower(got), "set-cookie") ||
			strings.Contains(got, "evil") {
			t.Errorf("%s: %q", path, got)
		}
	}
}

// Request trailers never reach the application: a chunked request's trailer section is
// not merged into the headers.
func TestRequestTrailersIgnored(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	got := raw(t, ts, "POST /echo HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\nTrailer: X-Smuggle\r\n"+
		"Connection: close\r\n\r\n5\r\nhello\r\n0\r\nX-Smuggle: 1\r\nX-Other: 2\r\n\r\n")
	if !strings.HasPrefix(got, "HTTP/1.1 200 ") || !strings.Contains(got, `"len": 5`) ||
		strings.Contains(strings.ToLower(got), "x-smuggle") || strings.Contains(strings.ToLower(got), "x-other") {
		t.Errorf("%q", got)
	}
}

// raw sends a request over a plain connection and returns the whole response.
func raw(t *testing.T, ts *httptest.Server, request string) string {
	t.Helper()
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, request); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(bufio.NewReader(c))
	return string(b)
}

// TestHTTP2HostField: a host field that differs from :authority (RFC 9113 §8.3.1:
// malformed) is refused; one that matches is the single host header the app sees.
func TestHTTP2HostField(t *testing.T) {
	ts := httptest.NewUnstartedServer(srv)
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "https://")
	tlsConf := ts.Client().Transport.(*http.Transport).TLSClientConfig
	for _, tc := range []struct {
		host   string
		status int
	}{{"evil.example", 400}, {"good.example", 200}} {
		resp, err := h2raw.Do(addr, tlsConf, [][2]string{{":method", "GET"}, {":scheme", "https"},
			{":authority", "good.example"}, {":path", "/echo"}, {"host", tc.host}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != tc.status || strings.Contains(string(resp.Body), "evil") {
			t.Errorf("host %s: %d %s", tc.host, resp.Status, resp.Body)
		}
	}
}
