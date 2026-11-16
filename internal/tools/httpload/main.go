// Command httpload is a minimal HTTP/1.1 keep-alive load generator for pythago's server
// benchmarks: each connection sends one request at a time and reads the response (by
// Content-Length), for a fixed duration after a warm-up. A response with "Connection:
// close" (servers without keep-alive, such as gunicorn's sync worker) makes the
// connection redial; the reconnect is part of the next request's latency.
//
//	httpload -c 64 -d 10s -w 2s http://127.0.0.1:8000/path
//	httpload -ca ca.pem -body 16384 https://127.0.0.1:8443/upload
//
// An https URL speaks TLS (HTTP/1.1 by ALPN; -ca trusts a CA file instead of the system's
// roots). -body N sends a POST with N bytes. -close asks for "Connection: close", so every
// request opens a connection (and, over TLS, makes a full handshake: no session
// resumption).
//
// It prints requests per second, latency percentiles, the TLS version and cipher suite,
// and the slowest connect (TCP, and the handshake over TLS) with the number of connects
// over a second, as one JSON line. A connect over a second is a lost SYN or a server
// that did not accept in time; its wait counts in no request's latency until it is done.
package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	conns := flag.Int("c", 64, "connections")
	dur := flag.Duration("d", 10*time.Second, "measured duration")
	warm := flag.Duration("w", 2*time.Second, "warm-up duration (not measured)")
	label := flag.String("label", "", "label to include in the output")
	caFile := flag.String("ca", "", "CA certificates (PEM) to trust for an https URL")
	body := flag.Int("body", 0, "send a POST with this many bytes instead of a GET")
	closing := flag.Bool("close", false, `send "Connection: close": a new connection per request`)
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: httpload [flags] http://host:port/path")
	}
	u, err := url.Parse(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	head := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: httpload\r\n", u.RequestURI(), u.Host)
	if *body > 0 {
		head = fmt.Sprintf("POST %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: httpload\r\n"+
			"Content-Type: application/octet-stream\r\nContent-Length: %d\r\n", u.RequestURI(), u.Host, *body)
	}
	if *closing {
		head += "Connection: close\r\n"
	}
	req := append([]byte(head+"\r\n"), make([]byte, *body)...)
	dial := func() (net.Conn, error) { return net.Dial("tcp", u.Host) }
	var tlsState atomic.Pointer[tls.ConnectionState]
	switch u.Scheme {
	case "http":
	case "https":
		cfg := &tls.Config{ServerName: u.Hostname(), NextProtos: []string{"http/1.1"}}
		if *caFile != "" {
			pem, err := os.ReadFile(*caFile)
			if err != nil {
				log.Fatal(err)
			}
			cfg.RootCAs = x509.NewCertPool()
			if !cfg.RootCAs.AppendCertsFromPEM(pem) {
				log.Fatalf("%s: no certificates", *caFile)
			}
		}
		dial = func() (net.Conn, error) {
			c, err := tls.Dial("tcp", u.Host, cfg)
			if err == nil && tlsState.Load() == nil {
				st := c.ConnectionState()
				tlsState.Store(&st)
			}
			return c, err
		}
	default:
		log.Fatalf("unsupported scheme %q", u.Scheme)
	}

	var slowDials, maxDial atomic.Int64 // connects over a second; the slowest, in µs
	timedDial := func() (net.Conn, error) {
		t0 := time.Now()
		c, err := dial()
		d := time.Since(t0)
		if d > time.Second {
			slowDials.Add(1)
		}
		for m := maxDial.Load(); d.Microseconds() > m && !maxDial.CompareAndSwap(m, d.Microseconds()); m = maxDial.Load() {
		}
		return c, err
	}
	var measuring, stop atomic.Bool
	var errs, bytes atomic.Int64
	lat := make([][]time.Duration, *conns)
	var wg sync.WaitGroup
	for i := range *conns {
		wg.Go(func() {
			var c net.Conn
			var r *bufio.Reader
			defer func() {
				if c != nil {
					c.Close()
				}
			}()
			for !stop.Load() {
				t0 := time.Now()
				if c == nil {
					var err error
					if c, err = timedDial(); err != nil {
						errs.Add(1)
						return
					}
					r = bufio.NewReaderSize(c, 64<<10)
				}
				if _, err := c.Write(req); err != nil {
					errs.Add(1)
					return
				}
				n, closed, err := readResponse(r)
				if err != nil {
					errs.Add(1)
					return
				}
				if closed || *closing {
					c.Close()
					c = nil
				}
				if measuring.Load() {
					lat[i] = append(lat[i], time.Since(t0))
					bytes.Add(n)
				}
			}
		})
	}
	time.Sleep(*warm)
	measuring.Store(true)
	t0 := time.Now()
	time.Sleep(*dur)
	measuring.Store(false)
	elapsed := time.Since(t0)
	stop.Store(true)
	wg.Wait()

	var all []time.Duration
	for _, l := range lat {
		all = append(all, l...)
	}
	slices.Sort(all)
	pct := func(p float64) float64 {
		if len(all) == 0 {
			return 0
		}
		return float64(all[min(len(all)-1, int(p*float64(len(all))))].Microseconds()) / 1000
	}
	out := map[string]any{
		"label": *label, "conns": *conns, "requests": len(all),
		"rps": float64(len(all)) / elapsed.Seconds(), "mbps": float64(bytes.Load()) / elapsed.Seconds() / 1e6,
		"p50_ms": pct(0.5), "p90_ms": pct(0.9), "p99_ms": pct(0.99), "errors": errs.Load(),
		"max_connect_ms": float64(maxDial.Load()) / 1000, "slow_connects": slowDials.Load(),
	}
	if st := tlsState.Load(); st != nil {
		out["tls"] = tls.VersionName(st.Version) + " " + tls.CipherSuiteName(st.CipherSuite)
		out["alpn"] = st.NegotiatedProtocol
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

// readResponse reads one response and returns its body size, and whether the server
// closes the connection after it.
func readResponse(r *bufio.Reader) (int64, bool, error) {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return 0, false, err
	}
	if !strings.HasPrefix(string(line), "HTTP/1.1 200") {
		return 0, false, fmt.Errorf("status %q", strings.TrimSpace(string(line)))
	}
	length := int64(-1)
	closing := false
	for {
		line, err = r.ReadSlice('\n')
		if err != nil {
			return 0, false, err
		}
		h := strings.TrimSpace(string(line))
		if h == "" {
			break
		}
		k, v, ok := strings.Cut(h, ":")
		switch {
		case !ok:
		case strings.EqualFold(k, "content-length"):
			if length, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
				return 0, false, err
			}
		case strings.EqualFold(k, "connection"):
			closing = strings.EqualFold(strings.TrimSpace(v), "close")
		}
	}
	if length < 0 {
		return 0, false, fmt.Errorf("no Content-Length")
	}
	_, err = io.CopyN(io.Discard, r, length)
	return length, closing, err
}
