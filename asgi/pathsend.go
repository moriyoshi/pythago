package asgi

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/moriyoshi/pythago/py"
)

// The http.response.pathsend extension: the application names a file and Go sends it,
// with sendfile(2) when the response is not chunked (net/http's ReadFrom path).
//
//   - The path must be absolute. It is opened once, O_NONBLOCK|O_NOCTTY|O_CLOEXEC (a FIFO
//     cannot block the open), and everything after is decided on that descriptor: fstat
//     must say regular file (no /dev/zero, FIFOs or directories), and its size at open is
//     the size sent.
//   - Options.PathSendRoots, when set, confines paths to those directories through
//     os.Root, which refuses ".." and symlink escapes when resolving (openat2
//     RESOLVE_BENEATH on Linux). Without it any file the process can read may be sent:
//     the application could read it itself.
//   - The size becomes the Content-Length unless trailers follow (chunked); an
//     application's own Content-Length must match it. Exactly that many bytes are sent:
//     a file that shrinks meanwhile aborts the connection rather than send a short body.

// openForSend opens path for http.response.pathsend.
func (s *Server) openForSend(path string) (*os.File, int64, error) {
	if !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return nil, 0, fmt.Errorf("pathsend needs an absolute path, not %q", path)
	}
	const flags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOCTTY
	var f *os.File
	var err error
	if len(s.roots) == 0 {
		f, err = os.OpenFile(path, flags, 0)
	} else {
		f, err = s.openInRoots(filepath.Clean(path), flags)
	}
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat() // fstat on the descriptor just opened
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("pathsend: %s is not a regular file", path)
	}
	return f, fi.Size(), nil
}

// openInRoots opens path through the first configured root containing it.
func (s *Server) openInRoots(path string, flags int) (*os.File, error) {
	for _, r := range s.roots {
		rel, err := filepath.Rel(r.dir, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return r.root.OpenFile(rel, flags, 0)
	}
	return nil, fmt.Errorf("pathsend: %s is outside the allowed roots", path)
}

// closeRoots closes the PathSendRoots directories (Close). An open directory cannot be
// removed on Windows.
func (s *Server) closeRoots() {
	for _, r := range s.roots {
		r.root.Close()
	}
}

type pathRoot struct {
	dir  string
	root *os.Root
}

func openRoots(dirs []string) ([]pathRoot, error) {
	var roots []pathRoot
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, err
		}
		r, err := os.OpenRoot(abs)
		if err != nil {
			return nil, fmt.Errorf("asgi: PathSendRoots: %w", err)
		}
		roots = append(roots, pathRoot{abs, r})
	}
	return roots, nil
}

// pathSendActions takes pathsend(server, h, status, headers, count, trailers, path):
// the held response head and the file, in one call.
func pathSendActions(q *request, a []*py.Object) (action, action, error) {
	st, err := startAction(q, a[2], a[3], a[4], a[5])
	if err != nil {
		return action{}, action{}, err
	}
	p, err := a[6].GoString()
	if err != nil {
		return action{}, action{}, err
	}
	f, size, err := q.srv.openForSend(p)
	if err != nil {
		return action{}, action{}, err
	}
	declared := false
	for _, kv := range st.headers {
		if strings.EqualFold(kv[0], "content-length") {
			declared = true
			if kv[1] != strconv.FormatInt(size, 10) {
				f.Close()
				return action{}, action{}, fmt.Errorf("content-length %s does not match %s (%d bytes)", kv[1], p, size)
			}
		}
	}
	if !declared && !st.trailers {
		st.headers = append(st.headers, [2]string{"content-length", strconv.FormatInt(size, 10)})
	}
	return st, action{kind: actPathSend, file: f, size: size}, nil
}

// sendFile writes exactly size bytes of f (sendfile when net/http can) and closes it.
func sendFile(w io.Writer, f *os.File, size int64) error {
	defer f.Close()
	n, err := io.CopyN(w, f, size)
	if err == nil && n != size {
		err = io.ErrUnexpectedEOF
	}
	if errors.Is(err, io.EOF) {
		err = fmt.Errorf("pathsend: %s shrank while being sent: %w", f.Name(), io.ErrUnexpectedEOF)
	}
	return err
}
