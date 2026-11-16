// Command wsgiserve serves a WSGI application with package wsgi behind net/http, like
// `gunicorn module:app`:
//
//	wsgiserve [-addr 127.0.0.1:8000] [-app-dir dir] [-threads n] [-script-name /prefix] [-processes n] [-cert c -key k] module:app
//
// -threads sets the admission slots (py.Config.Slots), which is how many requests run
// in Python at once. With -processes n above 1, the process listens and supervises n
// copies of itself that serve the listener (workers.Spawn); Python runs only in those.
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

	_ "github.com/moriyoshi/pythago/internal/testimage"
	"github.com/moriyoshi/pythago/py"
	"github.com/moriyoshi/pythago/workers"
	"github.com/moriyoshi/pythago/wsgi"
)

func main() {
	cfg := py.Config{Environment: os.Getenv("PYRUN_ENV")}
	if py.IsPythonCommand() {
		os.Exit(py.Main(cfg, os.Args))
	}
	addr := flag.String("addr", "127.0.0.1:8000", "listen address")
	appDir := flag.String("app-dir", ".", "directory the application is imported from")
	threads := flag.Int("threads", 0, "requests in Python at once (py.Config.Slots; default: min(4, GOMAXPROCS) with the GIL, GOMAXPROCS free-threaded)")
	scriptName := flag.String("script-name", "", "path prefix the application is mounted under (SCRIPT_NAME)")
	cert := flag.String("cert", "", "TLS certificate file (with -key: HTTPS and HTTP/2)")
	key := flag.String("key", "", "TLS key file")
	processes := flag.Int("processes", 1, "worker processes sharing the listener (workers.Spawn); the first process only supervises")
	cpuprofile := flag.String("cpuprofile", "", "write a Go CPU profile (all threads, Python's time as cgocall) until exit")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: wsgiserve [flags] module:app")
	}
	cfg.Files = os.DirFS(*appDir)
	cfg.Slots = *threads
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
	srv, err := wsgi.New(flag.Arg(0), wsgi.Options{ScriptName: *scriptName, MultiProcess: index >= 0})
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
	if err := srv.Close(); err != nil {
		log.Print(err)
	}
	if err := py.Finalize(); err != nil {
		log.Print(err)
	}
}
