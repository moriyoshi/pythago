package asgi_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
)

type pathFixture struct {
	root, file, outside, link, fifo string
	content                         []byte
}

func newPathFixture(t *testing.T) pathFixture {
	t.Helper()
	base := t.TempDir()
	f := pathFixture{root: filepath.Join(base, "root"), content: bytes.Repeat([]byte("0123456789abcdef"), 20000)}
	os.Mkdir(f.root, 0o755)
	f.file = filepath.Join(f.root, "data.bin")
	f.outside = filepath.Join(base, "secret.txt")
	f.link = filepath.Join(f.root, "escape")
	f.fifo = filepath.Join(f.root, "fifo")
	if err := os.WriteFile(f.file, f.content, 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f.outside, []byte("secret"), 0o644)
	if err := os.Symlink(f.outside, f.link); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		f.link = "" // symlinks need a privilege on Windows
	}
	if f.link != "" && runtime.GOOS == "windows" {
		// Under Wine, symlinks are Unix ones, which os.Root follows out of the root (on
		// Windows it refuses the reparse point): the platform cannot check that here.
		if r, err := os.OpenRoot(f.root); err == nil {
			if lf, err := r.Open("escape"); err == nil {
				lf.Close()
				t.Log("os.Root follows a symlink out of its root on this system (Wine): not checked")
				f.link = ""
			}
			r.Close()
		}
	}
	if err := mkfifo(f.fifo); errors.Is(err, errors.ErrUnsupported) {
		f.fifo = ""
	} else if err != nil {
		t.Fatal(err)
	}
	return f
}

func pathURL(ts *httptest.Server, path string, extra string) string {
	return ts.URL + "/p?path=" + url.QueryEscape(path) + extra
}

func TestPathSend(t *testing.T) {
	f := newPathFixture(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, body := get(t, ts, pathURL(ts, f.file, "")[len(ts.URL):])
	if resp.StatusCode != 200 || !bytes.Equal(body, f.content) || resp.ContentLength != int64(len(f.content)) {
		t.Errorf("status %d, %d bytes, content-length %d", resp.StatusCode, len(body), resp.ContentLength)
	}
	// HEAD: the length, no body.
	resp, err := ts.Client().Head(pathURL(ts, f.file, ""))
	if err != nil || resp.ContentLength != int64(len(f.content)) {
		t.Errorf("HEAD: %v %v", err, resp)
	}
	// The application's own Content-Length must match the file.
	if resp, _ := get(t, ts, pathURL(ts, f.file, "&cl="+strconv.Itoa(len(f.content)))[len(ts.URL):]); resp.StatusCode != 200 {
		t.Errorf("matching content-length: %d", resp.StatusCode)
	}
	// Refused before anything is sent: a clean 500.
	start := time.Now()
	refused := []string{
		pathURL(ts, f.file, "&cl=5"),        // content-length mismatch
		pathURL(ts, "pathsend_test.go", ""), // relative (existing in the working directory)
		pathURL(ts, f.root, ""),             // directory
		pathURL(ts, "/dev/zero", ""),        // a device: endless
		pathURL(ts, f.root+"/missing", ""),  // missing
	}
	if f.fifo != "" {
		refused = append(refused, pathURL(ts, f.fifo, "")) // a FIFO: the open must not block
	}
	for _, c := range refused {
		resp, body := get(t, ts, c[len(ts.URL):])
		if resp.StatusCode != 500 {
			t.Errorf("%s: %d %q", c, resp.StatusCode, truncate(body))
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("refusals took %v (a blocking open?)", d)
	}
	// After a body chunk the head is already sent: pathsend raises and the connection is
	// aborted (the client sees a truncated response, not a complete one).
	if _, _, err := getTE(t, ts.Client(), pathURL(ts, f.file, "&after=1"), false); err == nil {
		t.Error("pathsend after a body chunk completed normally")
	}
	// With trailers: chunked, the file, then the trailers.
	resp, sbody, err := getTE(t, ts.Client(), pathURL(ts, f.file, "&trailers=1"), true)
	if err != nil || sbody != string(f.content) || resp.Trailer.Get("X-Sent") != "file" || resp.ContentLength != -1 {
		t.Errorf("trailers: %v, %d bytes, trailer %v", err, len(sbody), resp.Trailer)
	}
}

// PathSendRoots confines pathsend: no files outside, no ".." and no symlink escapes.
func TestPathSendRoots(t *testing.T) {
	f := newPathFixture(t)
	confined, err := asgi.New("testapp:app", asgi.Options{PathSendRoots: []string{f.root}})
	if err != nil {
		t.Fatal(err)
	}
	defer confined.Close(context.Background())
	ts := httptest.NewServer(confined)
	defer ts.Close()
	if resp, body := get(t, ts, pathURL(ts, f.file, "")[len(ts.URL):]); resp.StatusCode != 200 || len(body) != len(f.content) {
		t.Errorf("inside the root: %d, %d bytes", resp.StatusCode, len(body))
	}
	for _, p := range []string{f.outside, f.root + "/../secret.txt", f.link} {
		if p == "" {
			continue // no symlink (Windows)
		}
		resp, body := get(t, ts, pathURL(ts, p, "")[len(ts.URL):])
		if resp.StatusCode != 500 || bytes.Contains(body, []byte("secret")) {
			t.Errorf("%s: %d %q", p, resp.StatusCode, body)
		}
	}
	// Unconfined, the same symlink is followed (the application chose the path).
	if f.link == "" {
		return
	}
	ts2 := httptest.NewServer(srv)
	defer ts2.Close()
	if resp, body := get(t, ts2, pathURL(ts2, f.link, "")[len(ts2.URL):]); resp.StatusCode != 200 || string(body) != "secret" {
		t.Errorf("unconfined symlink: %d %q", resp.StatusCode, body)
	}
	_ = http.MethodGet
}
