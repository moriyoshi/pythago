// Package pbs locates python-build-standalone (PBS) release assets: verified downloads
// and extraction come from package ghrelease.
package pbs

import (
	"fmt"
	"strings"

	"github.com/moriyoshi/pythago/internal/ghrelease"
)

// DefaultRelease is the PBS release pythago is tested with.
const DefaultRelease = "20261110"

// Asset is a release asset.
type Asset = ghrelease.Asset

// Release is one PBS release, with a download cache directory.
type Release struct {
	Tag   string // e.g. "20261110"
	Cache string // download and extraction cache
	Log   func(format string, args ...any)
}

func (r Release) gh() ghrelease.Release {
	return ghrelease.Release{Repo: "astral-sh/python-build-standalone", Tag: r.Tag, Cache: r.Cache, Log: r.Log}
}

// Find returns the asset for Python version ("3.13"), target triple and flavor (e.g.
// "pgo+lto-full", "install_only_stripped"; with a "freethreaded" prefix for free-threaded
// builds, joined by "+" for full archives and "-" for install_only ones).
func (r Release) Find(version, triple, flavor string) (Asset, error) {
	assets, err := r.gh().Assets()
	if err != nil {
		return Asset{}, err
	}
	prefix := "cpython-" + version + "."
	suffix := "-" + triple + "-" + flavor + ".tar."
	for _, a := range assets {
		if strings.HasPrefix(a.Name, prefix) && strings.Contains(a.Name, suffix) &&
			(strings.HasSuffix(a.Name, ".tar.zst") || strings.HasSuffix(a.Name, ".tar.gz")) {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("PBS release %s has no %s*%s*", r.Tag, prefix, suffix)
}

// Download fetches a into the cache unless a verified copy is there, and returns its path.
func (r Release) Download(a Asset) (string, error) { return r.gh().Download(a) }

// Extract downloads and extracts a (once) and returns the directory holding its top-level
// "python" directory.
func (r Release) Extract(a Asset) (string, error) { return r.gh().Extract(a) }

// Triple is the PBS target triple for goos/goarch.
func Triple(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/arm64":
		return "aarch64-unknown-linux-gnu", nil
	case "linux/amd64":
		return "x86_64-unknown-linux-gnu", nil
	case "darwin/arm64":
		return "aarch64-apple-darwin", nil
	case "darwin/amd64":
		return "x86_64-apple-darwin", nil
	case "windows/arm64":
		return "aarch64-pc-windows-msvc", nil
	case "windows/amd64":
		return "x86_64-pc-windows-msvc", nil
	}
	return "", fmt.Errorf("unsupported target %s/%s", goos, goarch)
}
