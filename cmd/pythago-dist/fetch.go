package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/moriyoshi/pythago/internal/pbs"
)

// fetch downloads the PBS full archive for o (verified against the digest GitHub
// publishes for the release asset), extracts it, and returns the directory containing
// PYTHON.json.
func fetch(o *options) (string, error) {
	flavor := "pgo+lto-full"
	if o.variant == "freethreaded" {
		flavor = "freethreaded+pgo+lto-full"
	}
	rel := pbs.Release{Tag: o.release, Cache: o.cache, Log: log.Printf}
	a, err := rel.Find(o.python, o.triple, flavor)
	if err != nil {
		return "", err
	}
	dir, err := rel.Extract(a)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "python"), nil
}

// execTool runs a producer tool with its output on stderr.
func execTool(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
	}
	return nil
}

// toolOutput runs a producer tool and returns its trimmed standard output.
func toolOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// execToolIn runs a producer tool in dir.
func execToolIn(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
	}
	return nil
}
