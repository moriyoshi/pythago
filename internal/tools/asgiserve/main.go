// Command asgiserve serves an ASGI application with package asgi behind net/http, like
// `uvicorn module:app`:
//
//	asgiserve [-addr 127.0.0.1:8000] [-app-dir dir] [-workers n] [-loop-thread] [-processes n] [-cert c -key k] module:app
//
// With -processes n above 1, the process listens and supervises n copies of itself that
// serve the listener (workers.Spawn); Python runs only in those.
//
// With -cert and -key it serves HTTPS, HTTP/2 included; WebSockets over HTTP/2 (RFC 8441)
// also need GODEBUG=http2xconnect=1 in the environment (Go reads it at start-up).
//
// PYRUN_ENV=dir uses a venv-like environment (Config.Environment). The native image and
// stdlib come from the test image, .work/testimage (built with GOWORK=.work/go.work).
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"runtime/pprof"
	"strconv"
	"time"

	"github.com/moriyoshi/pythago/asgi"
	_ "github.com/moriyoshi/pythago/internal/testimage"
	"github.com/moriyoshi/pythago/py"
	"github.com/moriyoshi/pythago/workers"
)

func main() {
	cfg := py.Config{Environment: os.Getenv("PYRUN_ENV")}
	asgi.Configure(&cfg)
	if py.IsPythonCommand() {
		os.Exit(py.Main(cfg, os.Args))
	}
	addr := flag.String("addr", "127.0.0.1:8000", "listen address")
	appDir := flag.String("app-dir", ".", "directory the application is imported from")
	loops := flag.Int("workers", 0, "event loops (default: 1 with the GIL, GOMAXPROCS free-threaded)")
	cert := flag.String("cert", "", "TLS certificate file (with -key: HTTPS and HTTP/2)")
	key := flag.String("key", "", "TLS key file")
	loopThread := flag.Bool("loop-thread", false, "run every request on its worker's loop thread instead of inline (asgi.Options.LoopThread)")
	processes := flag.Int("processes", 1, "worker processes sharing the listener (workers.Spawn); the first process only supervises")
	cpuprofile := flag.String("cpuprofile", "", "write a Go CPU profile (all threads, Python's time as cgocall) until exit")
	fusedBytes := flag.Int("fused-bytes", 0, "py.Config.FusedBytes: request bodies in Go-owned bytes objects, with at most this many bytes of chunks (0: off)")
	flag.Parse()
	cfg.FusedBytes = *fusedBytes
	if flag.NArg() != 1 {
		log.Fatal("usage: asgiserve [flags] module:app")
	}
	cfg.Files = os.DirFS(*appDir)
	index := workers.Index()
	l, err := workers.InheritedListener()
	if err != nil {
		log.Fatal(err)
	}
	if l == nil {
		if l, err = net.Listen("tcp", *addr); err != nil {
			log.Fatal(err)
		}
		if *processes > 1 { // supervise: Python is never started in this process
			if err := workers.Spawn(context.Background(), l, *processes); err != nil {
				log.Fatal(err)
			}
			return
		}
	}
	if *cpuprofile != "" {
		path := *cpuprofile
		if index >= 0 {
			path += "." + strconv.Itoa(index)
		}
		f, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	if err := py.Init(cfg); err != nil {
		log.Fatal(err)
	}
	srv, err := asgi.New(flag.Arg(0), asgi.Options{Workers: *loops, LoopThread: *loopThread})
	if err != nil {
		log.Fatal(err)
	}
	// Header and idle timeouts against slowloris clients; no read or write timeout,
	// which would cut streaming responses (and WebSockets) short.
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	ctx, stop := workers.NotifyContext(context.Background()) // SIGINT, SIGTERM, or Spawn's stop pipe (Windows)
	defer stop()
	go func() {
		<-ctx.Done()
		hs.Shutdown(context.Background())
	}()
	serve := func() error { return hs.Serve(l) }
	if *cert != "" {
		serve = func() error { return hs.ServeTLS(l, *cert, *key) }
	}
	if err := serve(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	if err := srv.Close(context.Background()); err != nil {
		log.Print(err)
	}
	if err := py.Finalize(); err != nil {
		log.Print(err)
	}
}
