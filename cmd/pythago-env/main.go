// Command pythago-env provisions a Python environment (a venv) for a program that embeds
// Python with package py, using a stock python-build-standalone Python of the same
// version and variant, and runs pip in it.
//
//	pythago-env -python 3.13 -env ./venv install torch --index-url https://download.pytorch.org/whl/cu130
//
// The program then uses it with py.Config{Environment: "./venv"}. Arguments after the
// flags go to pip; without any, the environment is only created.
package main

import (
	"flag"
	"log"

	"github.com/moriyoshi/pythago/env"
	"github.com/moriyoshi/pythago/internal/pbs"
)

func main() {
	var o env.Options
	dir := flag.String("env", "", "environment directory (created if missing)")
	flag.StringVar(&o.Python, "python", "3.13", "Python version of the native image (major.minor)")
	flag.BoolVar(&o.FreeThreaded, "freethreaded", false, "the native image is free-threaded")
	flag.StringVar(&o.Release, "release", pbs.DefaultRelease, "python-build-standalone release of the native image")
	flag.StringVar(&o.Interpreter, "interpreter", "", "stock python3 to provision with (default: fetch the matching PBS build)")
	flag.Parse()
	if *dir == "" {
		log.Fatal("pythago-env: -env is required")
	}
	if err := env.Create(*dir, o); err != nil {
		log.Fatal(err)
	}
	if flag.NArg() > 0 {
		if err := env.Pip(*dir, o, flag.Args()...); err != nil {
			log.Fatal(err)
		}
	}
}
