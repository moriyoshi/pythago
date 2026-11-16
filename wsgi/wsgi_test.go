package wsgi_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moriyoshi/pythago/internal/h2raw"
	_ "github.com/moriyoshi/pythago/internal/testimage" // registers the native image and stdlib
	"github.com/moriyoshi/pythago/internal/testwatch"
	"github.com/moriyoshi/pythago/py"
	"github.com/moriyoshi/pythago/wsgi"
)

const slots = 8

var srv, validatedSrv, mountedSrv *wsgi.Server

func config() py.Config {
	cfg := py.Config{Slots: slots, Files: fstest.MapFS{"testapp.py": {Data: []byte(testapp)}, "ioapp.py": {Data: []byte(ioapp)}}}
	// PYTHAGO_TEST_FUSION=1 runs the tests with the fusion on (Config.FusedBytes, 16 MiB).
	if os.Getenv("PYTHAGO_TEST_FUSION") == "1" {
		cfg.FusedBytes = 16 << 20
	}
	return cfg
}

func TestMain(m *testing.M) {
	if py.IsPythonCommand() {
		os.Exit(py.Main(config(), os.Args))
	}
	disarm := testwatch.Arm("py.Init and wsgi.New") // -test.timeout covers only m.Run
	if err := py.Init(config()); err != nil {
		fmt.Println("Init:", err)
		os.Exit(1)
	}
	var err error
	if srv, err = wsgi.New("testapp:app", wsgi.Options{}); err == nil {
		if validatedSrv, err = wsgi.New("testapp:validated", wsgi.Options{}); err == nil {
			mountedSrv, err = wsgi.New("testapp:app", wsgi.Options{ScriptName: "/mount"})
		}
	}
	if err != nil {
		fmt.Println("wsgi.New:", err)
		os.Exit(1)
	}
	disarm()
	code := m.Run()
	testwatch.Arm("Close and py.Finalize") // armed through os.Exit
	for _, s := range []*wsgi.Server{srv, validatedSrv, mountedSrv} {
		if err := s.Close(); err != nil {
			fmt.Println("Close:", err)
			code = 1
		}
	}
	if err := py.Finalize(); err != nil {
		fmt.Println("Finalize:", err)
		code = 1
	}
	os.Exit(code)
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: %v", url, err)
	}
	return resp, body
}

func envOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return env
}

// rawRequest sends req as is and returns the raw response bytes up to EOF or timeout.
func rawRequest(t *testing.T, addr, req string) string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(c)
	return string(b)
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestEnviron(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	body := []byte("hello=world")
	req, _ := http.NewRequest("POST", ts.URL+"/env?a=1&b=%20x", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("X-Multi", "one")
	req.Header.Add("X-Multi", "two")
	req.Header["X_Underscore"] = []string{"dropped"}
	req.Header.Set("Proxy", "http://evil.example")
	req.Header.Add("Cookie", "a=1")
	req.Header.Add("Cookie", "b=2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	env := envOf(t, b)
	host := strings.TrimPrefix(ts.URL, "http://")
	// The server's address, which the client connects from too: 127.0.0.1, or ::1 when
	// httptest found no free IPv4 loopback port.
	ip := ts.Listener.Addr().(*net.TCPAddr).IP.String()
	want := map[string]any{
		"REQUEST_METHOD": "POST", "PATH_INFO": "/env", "QUERY_STRING": "a=1&b=%20x", "SCRIPT_NAME": "",
		"REQUEST_URI": "/env?a=1&b=%20x", "SERVER_PROTOCOL": "HTTP/1.1", "CONTENT_TYPE": "application/x-www-form-urlencoded",
		"CONTENT_LENGTH": "11", "HTTP_HOST": host, "HTTP_X_MULTI": "one, two", "HTTP_COOKIE": "a=1; b=2",
		"SERVER_NAME": ip, "REMOTE_ADDR": ip, "wsgi.url_scheme": "http",
		"wsgi.multithread": true, "wsgi.multiprocess": false, "wsgi.run_once": false, "wsgi.input_terminated": true,
		"body_len": float64(len(body)), "body_sha256": sha(body),
	}
	for k, v := range want {
		if fmt.Sprint(env[k]) != fmt.Sprint(v) {
			t.Errorf("%s = %#v, want %#v", k, env[k], v)
		}
	}
	if fmt.Sprint(env["wsgi.version"]) != "[1 0]" {
		t.Errorf("wsgi.version = %v", env["wsgi.version"])
	}
	for _, k := range []string{"HTTP_X_UNDERSCORE", "HTTP_PROXY", "HTTP_CONTENT_TYPE", "HTTP_CONTENT_LENGTH"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s is in the environ", k)
		}
	}
	_, port, _ := net.SplitHostPort(host)
	if env["SERVER_PORT"] != port {
		t.Errorf("SERVER_PORT = %v, want %s", env["SERVER_PORT"], port)
	}
}

// TestEnvironMatchesWsgiref sends the same raw requests to wsgiref's server (the stock
// counterpart, on a Python thread of this process) and compares the environs. wsgiref
// listens on the loopback address httptest chose (::1 when it found no free IPv4 port),
// so that REMOTE_ADDR compares.
func TestEnvironMatchesWsgiref(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ours := strings.TrimPrefix(ts.URL, "http://")
	ip := ts.Listener.Addr().(*net.TCPAddr).IP.String()
	var port int64
	if err := py.Do(func() error {
		m, err := py.Import("testapp")
		if err != nil {
			return err
		}
		defer m.Close()
		r, err := m.CallMethod("run_wsgiref", ip)
		if err != nil {
			return err
		}
		defer r.Close()
		port, err = r.Int64()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stock := net.JoinHostPort(ip, strconv.FormatInt(port, 10))
	requests := []string{
		"GET /env HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n",
		"GET /env?x=%2F&y=caf%C3%A9 HTTP/1.1\r\nHost: example.com:8080\r\nX-Custom: v\r\nUser-Agent: test/1\r\nConnection: close\r\n\r\n",
		"GET /env/caf%C3%A9/a%2Fb/../c HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n",
		"POST /env HTTP/1.1\r\nHost: h\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 5\r\nConnection: close\r\n\r\nhello",
		"GET /env HTTP/1.1\r\nHost: h\r\nX_Underscore: 1\r\nX-Latin: caf\xe9\r\nConnection: close\r\n\r\n",
		"GET /env HTTP/1.0\r\nHost: h\r\n\r\n",
	}
	// PEP 3333's keys, the CGI variables it takes over and the request headers. wsgiref
	// also copies os.environ in, and adds CGI variables PEP 3333 does not ask for.
	compared := func(k string) bool {
		switch k {
		case "REQUEST_METHOD", "SCRIPT_NAME", "PATH_INFO", "QUERY_STRING", "CONTENT_TYPE", "CONTENT_LENGTH",
			"SERVER_PROTOCOL", "REMOTE_ADDR", "wsgi.url_scheme", "wsgi.version", "wsgi.multiprocess",
			"wsgi.run_once", "body_len", "body_sha256":
			return true
		}
		// HTTP_HOST: ours is the Host as sent. HTTP_X_UNDERSCORE: the one intended
		// difference, a header with "_" that wsgiref passes on and we drop.
		return strings.HasPrefix(k, "HTTP_") && k != "HTTP_HOST" && k != "HTTP_X_UNDERSCORE"
	}
	norm := func(env map[string]any) {
		if env["CONTENT_LENGTH"] == "" { // "may be empty or absent"; wsgiref sends ""
			delete(env, "CONTENT_LENGTH")
		}
		if _, ok := env["CONTENT_LENGTH"]; !ok && env["CONTENT_TYPE"] == "text/plain" {
			delete(env, "CONTENT_TYPE") // wsgiref's default for a request without one
		}
	}
	for _, req := range requests {
		parse := func(addr string) map[string]any {
			raw := rawRequest(t, addr, req)
			resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(raw)), nil)
			if err != nil {
				t.Fatalf("%s %q: %v\n%s", addr, req, err, raw)
			}
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("%q: %d %s", req, resp.StatusCode, b)
			}
			return envOf(t, b)
		}
		got, want := parse(ours), parse(stock)
		norm(got)
		norm(want)
		if strings.Contains(req, "X_Underscore") && (want["HTTP_X_UNDERSCORE"] != "1" || got["HTTP_X_UNDERSCORE"] != nil) {
			t.Errorf("X_Underscore: ours %v, wsgiref %v", got["HTTP_X_UNDERSCORE"], want["HTTP_X_UNDERSCORE"])
		}
		for k, v := range want {
			if compared(k) && fmt.Sprint(got[k]) != fmt.Sprint(v) {
				t.Errorf("%q: %s = %#v, wsgiref %#v", req, k, got[k], v)
			}
		}
		for k, v := range got {
			if _, ok := want[k]; !ok && compared(k) {
				t.Errorf("%q: %s = %#v, which wsgiref does not set", req, k, v)
			}
		}
	}
}

func TestScriptName(t *testing.T) {
	ts := httptest.NewServer(mountedSrv)
	defer ts.Close()
	_, b := get(t, ts.URL+"/mount/env")
	env := envOf(t, b)
	if env["SCRIPT_NAME"] != "/mount" || env["PATH_INFO"] != "/env" {
		t.Errorf("SCRIPT_NAME %v, PATH_INFO %v", env["SCRIPT_NAME"], env["PATH_INFO"])
	}
	for _, path := range []string{"/env", "/mountx/env", "/"} {
		if resp, _ := get(t, ts.URL+path); resp.StatusCode != 404 {
			t.Errorf("%s: %d, want 404 outside the mount", path, resp.StatusCode)
		}
	}
	if resp, _ := get(t, ts.URL+"/mount"); resp.StatusCode != 404 { // PATH_INFO "", the app's 404
		t.Errorf("/mount: %d", resp.StatusCode)
	}
}

// TestPathEncoding checks PEP 3333's latin-1 "bytes as str" environ, and that a NUL in
// the decoded path cannot shift the packed environ.
func TestPathEncoding(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, _ := get(t, ts.URL+"/latin1/caf%C3%A9")
	if resp.Header.Get("X-Path") != "/latin1/caf\xc3\xa9" || resp.Header.Get("X-Utf8") != "/latin1/caf\xe9" {
		t.Errorf("X-Path %q X-Utf8 %q", resp.Header.Get("X-Path"), resp.Header.Get("X-Utf8"))
	}
	resp, b := get(t, ts.URL+"/env%00HTTP_X_ADMIN%00yes")
	if resp.StatusCode != 400 || strings.Contains(string(b), "HTTP_X_ADMIN") {
		t.Errorf("NUL in the path: %d %s", resp.StatusCode, b)
	}
}

func TestBodyStreamed(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	data := make([]byte, 5<<20)
	rand.Read(data)
	// No Content-Length: chunked, streamed through wsgi.input.
	resp, err := http.Post(ts.URL+"/env", "application/octet-stream", io.MultiReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	env := envOf(t, b)
	if env["body_sha256"] != sha(data) {
		t.Errorf("streamed body: %v bytes, sha mismatch", env["body_len"])
	}
	if _, ok := env["CONTENT_LENGTH"]; ok {
		t.Errorf("CONTENT_LENGTH %v for a chunked body", env["CONTENT_LENGTH"])
	}
}

func TestBodyPieces(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	var lines bytes.Buffer
	for i := range 2000 {
		fmt.Fprintf(&lines, "line %d %s\n", i, strings.Repeat("y", i%97))
	}
	data := lines.Bytes()
	for _, tc := range []struct {
		name string
		body io.Reader
		size int64
		hdr  map[string]string
	}{
		{"prefetched", bytes.NewReader(data[:30000]), 30000, nil},
		{"streamed (length known, over Prefetch)", bytes.NewReader(data), int64(len(data)), nil},
		{"streamed (chunked)", io.MultiReader(bytes.NewReader(data)), -1, nil},
		{"streamed (Expect: 100-continue)", bytes.NewReader(data[:30000]), 30000, map[string]string{"Expect": "100-continue"}},
	} {
		body := data
		if tc.size == 30000 {
			body = data[:30000]
		}
		req, _ := http.NewRequest("POST", ts.URL+"/read?n=7", tc.body)
		req.ContentLength = tc.size
		for k, v := range tc.hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var got struct {
			Sha   string
			Parts []int
			Lines int
			Again int
		}
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("%s: %v %s", tc.name, err, b)
		}
		firstLine := bytes.IndexByte(body[7:], '\n') + 1
		if got.Sha != sha(body) || got.Parts[0] != 7 || got.Parts[1] != firstLine || got.Lines != 3 || got.Again != 0 {
			t.Errorf("%s: %+v (want parts [7 %d], 3 lines, sha %s)", tc.name, got, firstLine, sha(body)[:8])
		}
	}
}

func TestResponses(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	chunk := bytes.Repeat(func() []byte {
		b := make([]byte, 256)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}(), 256)
	for _, tc := range []struct {
		path    string
		body    []byte
		length  int64 // -1: chunked
		headers map[string]string
	}{
		{"/stream?n=4", bytes.Repeat(chunk, 4), -1, nil},
		{"/list?n=4", bytes.Repeat(chunk, 4), 4 * 65536, nil},
		{"/list?n=0", nil, 0, nil},
		{"/write", []byte("abc"), -1, map[string]string{"Content-Type": "text/plain"}},
		{"/exc-info", []byte("replaced"), 8, map[string]string{"X-Second": "1", "X-First": ""}},
	} {
		resp, b := get(t, ts.URL+tc.path)
		if !bytes.Equal(b, tc.body) || resp.ContentLength != tc.length {
			t.Errorf("%s: %d bytes, length %d; want %d bytes, length %d", tc.path, len(b), resp.ContentLength, len(tc.body), tc.length)
		}
		for k, v := range tc.headers {
			if resp.Header.Get(k) != v {
				t.Errorf("%s: %s = %q, want %q", tc.path, k, resp.Header.Get(k), v)
			}
		}
		if tc.path == "/exc-info" && resp.StatusCode != 503 {
			t.Errorf("/exc-info: status %d, want the replacement 503", resp.StatusCode)
		}
	}
	// 204: no body, no Content-Length; a body for it is the application's error.
	resp, b := get(t, ts.URL+"/nocontent")
	if resp.StatusCode != 204 || len(b) != 0 || resp.Header.Get("Content-Length") != "" {
		t.Errorf("204: %d %q %q", resp.StatusCode, b, resp.Header.Get("Content-Length"))
	}
	if resp, _ := get(t, ts.URL+"/nocontent?body=x"); resp.StatusCode != 500 {
		t.Errorf("204 with a body: %d, want 500", resp.StatusCode)
	}
	// HEAD: the head a GET would get, no body.
	req, _ := http.NewRequest("HEAD", ts.URL+"/list?n=2", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != 2*65536 {
		t.Errorf("HEAD: %d, length %d", resp.StatusCode, resp.ContentLength)
	}
}

// TestStreamingFlushes: PEP 3333 says each yielded block is sent before the next is asked
// for. The second block waits for a request that the client makes only after reading the
// first.
func TestStreamingFlushes(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/gate")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "first" {
		t.Fatalf("first chunk: %q %v", first, err)
	}
	get(t, ts.URL+"/open-gate")
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != "second" {
		t.Errorf("rest: %q", rest)
	}
}

func closedList(t *testing.T, base string) []string {
	_, b := get(t, base+"/closed")
	var names []string
	json.Unmarshal(b, &names)
	return names
}

func waitClosed(t *testing.T, base, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range closedList(t, base) {
			if n == name {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("close() of %q was not called: %v", name, closedList(t, base))
}

func TestErrors(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for _, path := range []string{"/error", "/twice", "/nostart", "/notbytes", "/none"} {
		resp, b := get(t, ts.URL+path)
		if resp.StatusCode != 500 || string(b) != "Internal Server Error\n" {
			t.Errorf("%s: %d %q", path, resp.StatusCode, b)
		}
	}
	// After the head is out, an error aborts: the client must not see a complete response.
	for _, path := range []string{"/error-after", "/exc-info-late"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil {
			t.Errorf("%s: the truncated response read as complete", path)
		}
	}
	waitClosed(t, ts.URL, "error-after")
	get(t, ts.URL+"/close-ok")
	waitClosed(t, ts.URL, "close-ok")
}

func TestDisconnect(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/disconnect")
	if err != nil {
		t.Fatal(err)
	}
	io.CopyN(io.Discard, resp.Body, 1<<20)
	resp.Body.Close() // the client goes away mid-stream
	waitClosed(t, ts.URL, "disconnect")
}

// TestBadResponses: malformed statuses and headers are the application's errors: a 500,
// and nothing of them reaches the client.
func TestBadResponses(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")
	for _, c := range []string{
		"name-space", "name-colon", "value-crlf", "value-lf", "value-nul", "value-del", "non-latin1",
		"bytes-value", "list-item", "shifted-pairs", "tuple-headers", "hop-connection", "hop-te",
		"hop-upgrade", "hop-keepalive", "cl-twice", "cl-bad", "cl-space", "status-short", "status-nospace",
		"status-digits", "status-99", "status-101", "status-600", "status-crlf", "status-bytes",
	} {
		raw := rawRequest(t, addr, "GET /bad?case="+c+" HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n")
		if !strings.HasPrefix(raw, "HTTP/1.1 500 ") || strings.Contains(strings.ToLower(raw), "evil") ||
			strings.Contains(raw, "X-A") || strings.Contains(raw, "\r\nok") {
			t.Errorf("%s:\n%s", c, raw)
		}
	}
}

// TestContentLength: a body shorter or longer than the Content-Length the application
// declared never reads as complete, over HTTP/1.1 and HTTP/2.
func TestContentLength(t *testing.T) {
	plain := httptest.NewServer(srv)
	defer plain.Close()
	h2 := httptest.NewUnstartedServer(srv)
	h2.EnableHTTP2 = true
	h2.StartTLS()
	defer h2.Close()
	for _, s := range []*httptest.Server{plain, h2} {
		client := s.Client()
		for _, q := range []string{
			"declared=10&actual=5", "declared=10&actual=5&mode=list",
			"declared=5&actual=10", "declared=5&actual=10&mode=list",
		} {
			resp, err := client.Get(s.URL + "/cl?" + q)
			if err != nil {
				continue // refused outright: fine
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil {
				t.Errorf("%s %s: read %q as a complete response (%d)", resp.Proto, q, b, resp.StatusCode)
			}
		}
		resp, err := client.Get(s.URL + "/cl?declared=5&actual=5")
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(b) != "xxxxx" {
			t.Errorf("%s exact length: %q %v", resp.Proto, b, err)
		}
	}
}

func TestFileWrapper(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	data := make([]byte, 3<<20+123)
	rand.Read(data)
	path := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query  string
		want   []byte
		status int
	}{
		{"", data, 200},
		{"&seek=1000", data[1000:], 200},
		{"&seek=1000&cl=500", data[1000:1500], 200},
		{"&cl=" + fmt.Sprint(len(data)+1), nil, 500}, // longer than the file: a clean 500
	} {
		resp, b := get(t, ts.URL+"/file?path="+path+tc.query)
		if resp.StatusCode != tc.status || !bytes.Equal(b, tc.want) && tc.status == 200 {
			t.Errorf("file%s: %d, %d bytes", tc.query, resp.StatusCode, len(b))
		}
		if tc.status == 200 && resp.ContentLength != int64(len(tc.want)) {
			t.Errorf("file%s: length %d, want %d", tc.query, resp.ContentLength, len(tc.want))
		}
	}
	req, _ := http.NewRequest("HEAD", ts.URL+"/file?path="+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ContentLength != int64(len(data)) {
		t.Errorf("HEAD file: length %d", resp.ContentLength)
	}
	// Not regular files: read in blocks by the wrapper.
	if _, b := get(t, ts.URL+"/file-bytesio"); string(b) != strings.Repeat("from memory", 1000) {
		t.Errorf("BytesIO: %d bytes", len(b))
	}
	if _, b := get(t, ts.URL+"/file-pipe"); string(b) != strings.Repeat("from a pipe", 1000) {
		t.Errorf("pipe: %d bytes", len(b))
	}
}

// TestValidator runs requests through wsgiref.validate, which checks the server's side
// of PEP 3333 (environ, start_response, wsgi.input, wsgi.errors, close) as well as the
// application's; its warnings are errors.
func TestValidator(t *testing.T) {
	ts := httptest.NewServer(validatedSrv)
	defer ts.Close()
	for _, path := range []string{"/env", "/stream?n=2", "/list?n=2", "/write", "/close-ok", "/file-bytesio", "/exc-info"} {
		resp, b := get(t, ts.URL+path)
		if resp.StatusCode/100 == 5 && path != "/exc-info" || resp.StatusCode == 500 {
			t.Errorf("%s: %d %s", path, resp.StatusCode, b)
		}
	}
	resp, err := http.Post(ts.URL+"/read?n=3", "text/plain", strings.NewReader("abcdef\nline1\nline2\nline3\nrest"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("/read: %d %s", resp.StatusCode, b)
	}
}

// TestConcurrency: requests blocked in Python (time.sleep) run in parallel, one per
// admission slot.
func TestConcurrency(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	start := time.Now()
	var wg sync.WaitGroup
	idents := make(chan string, slots)
	for range slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, b := get(t, ts.URL+"/sleep?s=0.5")
			idents <- string(b)
		}()
	}
	wg.Wait()
	close(idents)
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("%d requests sleeping 0.5 s took %v", slots, d)
	}
	seen := map[string]bool{}
	for id := range idents {
		seen[id] = true
	}
	if len(seen) != slots {
		t.Errorf("%d Python threads served %d concurrent requests", len(seen), slots)
	}
}

// TestHTTP2RequestFields sends HTTP/2 requests Go's client would not: a host field that
// differs from :authority (RFC 9113 §8.3.1: malformed), and content-length values that are
// not one decimal number.
func TestHTTP2RequestFields(t *testing.T) {
	ts := httptest.NewUnstartedServer(srv)
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "https://")
	tlsConf := ts.Client().Transport.(*http.Transport).TLSClientConfig
	req := func(extra [][2]string, body []byte) *h2raw.Response {
		t.Helper()
		method := "GET"
		if body != nil {
			method = "POST"
		}
		fields := append([][2]string{{":method", method}, {":scheme", "https"}, {":authority", "good.example"}, {":path", "/env"}}, extra...)
		resp, err := h2raw.Do(addr, tlsConf, fields, body)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := req([][2]string{{"host", "evil.example"}}, nil)
	if resp.Status != 400 || strings.Contains(string(resp.Body), "evil") {
		t.Errorf("host differing from :authority: %d %s", resp.Status, resp.Body)
	}
	resp = req([][2]string{{"host", "good.example"}}, nil) // the same host twice is fine
	if env := envOf(t, resp.Body); resp.Status != 200 || env["HTTP_HOST"] != "good.example" {
		t.Errorf("matching host: %d %v", resp.Status, env["HTTP_HOST"])
	}
	// net/http reads "5, 100000" and "+5" as a Content-Length of 0 and resets the stream
	// when the 5-byte DATA frame arrives. That races the handler: usually the reset comes
	// first (no status), but on a busy host the response head can go out before it (status
	// 200, then RST_STREAM, no body). Either way the client got no complete response.
	for _, cl := range []string{"5, 100000", "+5", "05"} {
		resp = req([][2]string{{"content-length", cl}}, []byte("hello"))
		if resp.Status != 200 || resp.Reset {
			continue // refused: fine
		}
		env := envOf(t, resp.Body)
		if fmt.Sprint(env["CONTENT_LENGTH"]) != fmt.Sprint(env["body_len"]) && env["CONTENT_LENGTH"] != nil {
			t.Errorf("content-length %q: CONTENT_LENGTH %q for a %v-byte body", cl, env["CONTENT_LENGTH"], env["body_len"])
		}
	}
}

// TestSlowReaderDoesNotStall: a client that does not read a large response blocks only its
// own request. Body writes never happen with the thread state attached, which (with the
// GIL) would stop every Python thread until the client read.
func TestSlowReaderDoesNotStall(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "GET /many?n=16384 HTTP/1.1\r\nHost: h\r\n\r\n") // 16 MiB, never read
	time.Sleep(time.Second)                                            // the socket buffers fill
	done := make(chan error, 1)
	go func() {
		_, err := http.Get(ts.URL + "/env")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a request stalled behind a client that does not read")
	}
}

// TestTwoThreadsOneRequest: a helper thread waiting in Go for the request body while the
// application's thread writes must not deadlock them (and the GIL with them).
func TestTwoThreadsOneRequest(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	io.WriteString(c, "POST /duplex HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n")
	time.Sleep(time.Second) // the helper reads and the main thread writes meanwhile
	io.WriteString(c, "5\r\nhello\r\n0\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "main got hello" {
		t.Errorf("%q %v", b, err)
	}
}

// TestContextPerRequest: a ContextVar a request sets and never resets does not reach the
// next request served on the same Python thread.
func TestContextPerRequest(t *testing.T) {
	ts := httptest.NewServer(srv)
	defer ts.Close()
	setOn := map[string]bool{}
	overlap := 0
	for i := 0; i < 400 && overlap < 20; i++ {
		_, b := get(t, fmt.Sprintf("%s/ctx-set?user=user%d", ts.URL, i))
		setOn[string(b)] = true
		_, b = get(t, ts.URL+"/ctx-get")
		ident, user, _ := strings.Cut(string(b), " ")
		if setOn[ident] {
			overlap++
		}
		if user != "nobody" {
			t.Fatalf("a request on thread %s saw current_user %q from an earlier request", ident, user)
		}
	}
	if overlap == 0 {
		t.Skip("no request landed on a thread an earlier one used")
	}
}
