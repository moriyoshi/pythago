// Package ghrelease fetches GitHub release assets: it looks them up in the release
// metadata, downloads them into a cache, verifies the sha256 digest GitHub publishes for
// each asset, and extracts them.
package ghrelease

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Release is one GitHub release, with a download cache directory.
type Release struct {
	Repo  string // "owner/name"
	Tag   string
	Cache string // download and extraction cache
	Log   func(format string, args ...any)
}

// Asset is a release asset.
type Asset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	URL    string `json:"browser_download_url"`
}

func (r Release) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// Assets lists the release's assets, from the GitHub API (cached per release).
func (r Release) Assets() ([]Asset, error) {
	file := filepath.Join(r.Cache, "release-"+strings.ReplaceAll(r.Repo, "/", "_")+"-"+r.Tag+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		u := apiBase + "/repos/" + r.Repo + "/releases/tags/" + url.PathEscape(r.Tag)
		resp, err := get(u)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", u, resp.Status)
		}
		if data, err = io.ReadAll(resp.Body); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(r.Cache, 0o755); err == nil {
			os.WriteFile(file, data, 0o644)
		}
	}
	var rel struct {
		Assets []Asset `json:"assets"`
	}
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, err
	}
	return rel.Assets, nil
}

// Named returns the asset called name.
func (r Release) Named(name string) (Asset, error) {
	assets, err := r.Assets()
	if err != nil {
		return Asset{}, err
	}
	for _, a := range assets {
		if a.Name == name {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("%s release %s has no asset %s", r.Repo, r.Tag, name)
}

// Download fetches a into the cache unless a verified copy is there, and returns its path.
func (r Release) Download(a Asset) (string, error) {
	want, ok := strings.CutPrefix(a.Digest, "sha256:")
	if !ok {
		return "", fmt.Errorf("%s: no sha256 digest in the release metadata", a.Name)
	}
	if err := os.MkdirAll(r.Cache, 0o755); err != nil {
		return "", err
	}
	file := filepath.Join(r.Cache, a.Name)
	if got, err := sha256File(file); err == nil && got == want {
		return file, nil
	}
	r.logf("fetch %s", a.Name)
	if err := download(a.URL, file); err != nil {
		return "", err
	}
	if got, err := sha256File(file); err != nil || got != want {
		os.Remove(file)
		return "", fmt.Errorf("%s: sha256 %s, want %s (%v)", a.Name, got, want, err)
	}
	return file, nil
}

// Extract downloads and extracts a (once) into a directory named after it, and returns
// that directory. With members, only those archive paths (and the trees under them) are
// extracted; .tar.gz archives ignore members. .tar.gz is extracted in Go; .tar.zst (through zstd) and .tar.xz through
// tar.
func (r Release) Extract(a Asset, members ...string) (string, error) {
	dir := filepath.Join(r.Cache, strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(a.Name, ".tar.zst"), ".tar.gz"), ".tar.xz"))
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	file, err := r.Download(a)
	if err != nil {
		return "", err
	}
	r.logf("extract %s", a.Name)
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	switch {
	case strings.HasSuffix(file, ".tar.gz"):
		err = extractTarGz(file, tmp)
	case strings.HasSuffix(file, ".tar.xz"):
		cmd := exec.Command("tar", append([]string{"-C", tmp, "-xJf", file}, members...)...)
		cmd.Stderr = os.Stderr
		err = cmd.Run()
	default:
		// LLVM's archives use a 1 GiB long-distance window, beyond zstd's default limit.
		cmd := exec.Command("tar", append([]string{"-C", tmp, "-I", "zstd -d --long=31", "-xf", file}, members...)...)
		cmd.Stderr = os.Stderr
		err = cmd.Run()
	}
	if err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("extract %s: %w", a.Name, err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	return dir, nil
}

func extractTarGz(file, dst string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe path %q in archive", h.Name)
		}
		p := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

// apiBase is the GitHub REST API root; requests to its host, and only those, carry the
// GITHUB_TOKEN (else GH_TOKEN) from the environment. Anonymous requests share a limit of
// 60 an hour per address, which shared CI runners exhaust. Tests point it at a local server.
var apiBase = "https://api.github.com"

// apiHost reports whether u is on the API host (host and port, exactly).
func apiHost(u *url.URL) bool {
	b, err := url.Parse(apiBase)
	return err == nil && u.Scheme == b.Scheme && u.Host == b.Host
}

func token() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
}

// client drops the token on a redirect off the API host. net/http drops Authorization
// itself only when the new host is neither the original one nor a subdomain of it, and
// compares host names without ports.
var client = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if !apiHost(req.URL) {
		req.Header.Del("Authorization")
	}
	return nil
}}

// get is http.Get, plus the token and the API version on requests to the API host.
func get(u string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if apiHost(req.URL) {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if t := token(); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	return client.Do(req)
}

func download(u, file string) error {
	resp, err := get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	tmp := file + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func sha256File(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
