package testwheels

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Server is an HTTP server for clients in the wheels (pyreqwest) to talk to:
//
//	/hello?i=N     "hello N"
//	/bytes?n=N     N bytes of the pattern 0, 1, ..., 255, 0, 1, ...
//	/echo          the request body
//	/slow?tag=T    holds the request until the client goes away or /release, counted
//	               per tag: inflight while held, then gone or released ("slow")
//	/release?tag=T answers the tag's held requests, and those that come later
//	/stats?tag=T   {"inflight": n, "gone": n, "released": n} for the tag
//
// The test's cleanup releases held requests and closes it.
type Server struct {
	*httptest.Server
	stop  chan struct{}
	mu    sync.Mutex
	stats map[string]*slowStats
}

type slowStats struct {
	Inflight int `json:"inflight"`
	Gone     int `json:"gone"`
	Released int `json:"released"`
	release  chan struct{}
}

// NewServer starts a Server for the test.
func NewServer(t testing.TB) *Server {
	s := &Server{stop: make(chan struct{}), stats: map[string]*slowStats{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello "+r.URL.Query().Get("i"))
	})
	mux.HandleFunc("/bytes", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		w.Header().Set("Content-Length", strconv.Itoa(n))
		buf := make([]byte, 1<<16)
		for i := range buf {
			buf[i] = byte(i)
		}
		for n > 0 {
			k := min(n, len(buf))
			if _, err := w.Write(buf[:k]); err != nil {
				return
			}
			n -= k
		}
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Write(body)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		st := s.slow(r.URL.Query().Get("tag"))
		s.mu.Lock()
		st.Inflight++
		s.mu.Unlock()
		var released, gone bool
		select {
		case <-r.Context().Done():
			gone = true
		case <-st.release:
			released = true
		case <-s.stop:
		case <-time.After(2 * time.Minute):
		}
		s.mu.Lock()
		st.Inflight--
		if gone {
			st.Gone++
		}
		if released {
			st.Released++
		}
		s.mu.Unlock()
		if !gone {
			io.WriteString(w, "slow")
		}
	})
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		st := s.slow(r.URL.Query().Get("tag"))
		s.mu.Lock()
		select {
		case <-st.release:
		default:
			close(st.release)
		}
		s.mu.Unlock()
		io.WriteString(w, "released")
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		st := s.slow(r.URL.Query().Get("tag"))
		s.mu.Lock()
		data, _ := json.Marshal(st)
		s.mu.Unlock()
		w.Write(data)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(func() {
		close(s.stop)
		s.Close()
	})
	return s
}

func (s *Server) slow(tag string) *slowStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats[tag]
	if st == nil {
		st = &slowStats{release: make(chan struct{})}
		s.stats[tag] = st
	}
	return st
}

// ClosedURL returns an http URL on a loopback port nothing listens on.
func ClosedURL(t testing.TB) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr + "/"
}

// Skip returns the site directory, or skips the test when PYTHAGO_TEST_WHEELS is unset.
func Skip(t testing.TB) string {
	t.Helper()
	site, err := Site()
	if err != nil {
		t.Fatal(err)
	}
	if site == "" {
		t.Skipf("%s is not set: no wheels (ci/test-wheels.txt, internal/tools/fetchwheels)", Env)
	}
	return site
}
