// Command pyrun runs a Python script through package py, with Python threads backed by
// goroutines.
//
//	pyrun script.py
//
// The native image and stdlib come from the test image, .work/testimage (cmd/pythago-dist,
// built with GOWORK=.work/go.work); set PYTHONHOME to use a stdlib installation on disk
// instead (needed for C extension builds that want Python's headers, such as
// torch.compile).
//
// The goaio module (package aio) is available. The environment (read once, at start,
// into settings):
//
//	PYRUN_ENV=dir          a venv-like environment (Config.Environment), with C
//	                       extensions loaded through py's extension shim
//	PYRUN_GO=n             also run the script's outer(g) from n goroutines through
//	                       py.Do, instead of from Python threads
//	PYRUN_CPUPROFILE=file  write a Go CPU profile of the run (Python's time shows as
//	                       runtime.cgocall)
//	PYRUN_TRACE=file       write a Go execution trace of the run
//
//	pyrun --python -m module args...
//
// runs a python command line (py.Main), as does a pyrun that Python started as
// sys.executable (multiprocessing workers).
package main

import (
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"sync"
	"time"

	"github.com/moriyoshi/pythago/aio"
	_ "github.com/moriyoshi/pythago/internal/testimage" // native image and stdlib
	"github.com/moriyoshi/pythago/py"
)

// settings holds what the environment asks for, read by configure before anything else
// runs.
var settings struct {
	env        string // PYRUN_ENV
	goroutines int    // PYRUN_GO
	cpuProfile string // PYRUN_CPUPROFILE
	trace      string // PYRUN_TRACE
}

// configure reads the environment into settings.
func configure() {
	settings.env = os.Getenv("PYRUN_ENV")
	if v := os.Getenv("PYRUN_GO"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Fatalf("pyrun: PYRUN_GO=%q: want a goroutine count", v)
		}
		settings.goroutines = n
	}
	settings.cpuProfile = os.Getenv("PYRUN_CPUPROFILE")
	settings.trace = os.Getenv("PYRUN_TRACE")
}

func main() {
	configure()
	cfg := py.Config{Environment: settings.env}
	aio.Configure(&cfg)
	if len(os.Args) > 1 && os.Args[1] == "--python" {
		os.Exit(py.Main(cfg, append([]string{os.Args[0]}, os.Args[2:]...)))
	}
	py.Run(cfg, func() error {
		defer profile()() // py.Run does not return: stop before it exits
		if len(os.Args) < 2 {
			return fmt.Errorf("usage: pyrun script.py")
		}
		// Like python: __main__ gets __file__ (multiprocessing's spawn and forkserver
		// re-import it in workers), sys.argv is the script and its arguments, and code
		// objects carry the script's path.
		if err := py.Do(func() error {
			sys, err := py.Import("sys")
			if err != nil {
				return err
			}
			defer sys.Close()
			if err := sys.SetAttr("argv", os.Args[1:]); err != nil {
				return err
			}
			g, err := py.From(map[string]any{"path": os.Args[1]})
			if err != nil {
				return err
			}
			defer g.Close()
			return py.ExecIn(runScript, g)
		}); err != nil {
			return err
		}
		if n := settings.goroutines; n > 0 {
			start := time.Now()
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for g := range n {
				wg.Go(func() {
					errs <- py.Do(func() error { return py.Exec(fmt.Sprintf("outer(%d)", g)) })
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					return err
				}
			}
			fmt.Printf("goroutines: %.2f s\n", time.Since(start).Seconds())
		}
		fmt.Printf("stats: %+v\n", py.ReadStats())
		return nil
	})
}

// profile starts the CPU profile and the trace that settings ask for and returns what
// stops them.
func profile() (stop func()) {
	var stops []func()
	if p := settings.cpuProfile; p != "" {
		f, err := os.Create(p)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
		stops = append(stops, pprof.StopCPUProfile)
	}
	if p := settings.trace; p != "" {
		f, err := os.Create(p)
		if err != nil {
			log.Fatal(err)
		}
		if err := trace.Start(f); err != nil {
			log.Fatal(err)
		}
		stops = append(stops, trace.Stop)
	}
	return func() {
		for _, s := range stops {
			s()
		}
	}
}

// runScript executes the script as python would: in __main__, with __file__ set.
const runScript = `
import sys
main = sys.modules["__main__"]
main.__file__ = path
with open(path, "rb") as f:
    code = compile(f.read(), path, "exec")
exec(code, vars(main))
`
