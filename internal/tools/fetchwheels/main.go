// Command fetchwheels downloads pinned binary wheels from PyPI for one pythago target, for
// tests that load real C extensions (internal/testwheels, PYTHAGO_TEST_WHEELS).
//
//	fetchwheels -python 3.13 -target linux/arm64 -cache DIR -o OUT pyreqwest==0.15.1 ...
//	fetchwheels -python 3.14 -variant freethreaded -target windows/amd64 -o OUT -r FILE
//
// For each name==version it reads PyPI's JSON API (https://pypi.org/pypi/NAME/VERSION/json),
// picks the binary wheel that the target's interpreter loads, downloads it into the cache
// (unless a copy with the published sha256 is there) and verifies it against that digest,
// then links or copies it into OUT. OUT then holds exactly the selected wheels, and a
// SHA256SUMS listing them. No sdist is ever chosen: a package without a matching wheel is
// an error.
//
// The choice, best first: a wheel for the interpreter's own ABI (cp313-cp313,
// cp314-cp314t), then the stable ABI (cp3X-abi3 with X up to the minor version; never for
// free-threaded builds, which cannot load it), then pure-Python wheels (py3-none-any).
// Among platforms: linux manylinux tags up to -glibc (default 2.28, the floor of py's
// extension shim; musllinux never), macOS tags up to -macos (default 11.0) for the
// architecture or universal2, win_amd64 or win_arm64.
//
// -r FILE reads further name==version lines (# comments allowed). The tool uses only the
// standard library, so it builds without the test image (GOWORK=off).
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("fetchwheels: ")
	python := flag.String("python", "", "Python version, e.g. 3.13")
	variant := flag.String("variant", "", `"freethreaded" for a free-threaded build`)
	target := flag.String("target", "", "GOOS/GOARCH, e.g. linux/arm64")
	cache := flag.String("cache", "", "download cache directory (default: fetchwheels under the user cache)")
	out := flag.String("o", "", "output directory for the selected wheels")
	reqs := flag.String("r", "", "file of name==version lines")
	glibc := flag.String("glibc", "2.28", "newest glibc a manylinux wheel may need")
	macos := flag.String("macos", "11.0", "newest macOS a wheel may need")
	flag.Parse()
	if *python == "" || *target == "" || *out == "" {
		log.Fatal("usage: fetchwheels -python X.Y [-variant freethreaded] -target GOOS/GOARCH -o DIR [-cache DIR] [-r FILE] name==version...")
	}
	pins := flag.Args()
	if *reqs != "" {
		more, err := readPins(*reqs)
		if err != nil {
			log.Fatal(err)
		}
		pins = append(pins, more...)
	}
	if len(pins) == 0 {
		log.Fatal("no name==version given")
	}
	tg, err := newTarget(*python, *variant, *target, *glibc, *macos)
	if err != nil {
		log.Fatal(err)
	}
	if *cache == "" {
		d, err := os.UserCacheDir()
		if err != nil {
			log.Fatal(err)
		}
		*cache = filepath.Join(d, "fetchwheels")
	}
	if err := os.MkdirAll(*cache, 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	var sums []string
	keep := map[string]bool{"SHA256SUMS": true}
	for _, pin := range pins {
		name, version, ok := strings.Cut(pin, "==")
		if !ok || name == "" || version == "" {
			log.Fatalf("%q: want name==version", pin)
		}
		f, err := fetch(*cache, name, version, tg)
		if err != nil {
			log.Fatal(err)
		}
		dst := filepath.Join(*out, f.Filename)
		if err := place(filepath.Join(*cache, f.Filename), dst, f.Digests.SHA256); err != nil {
			log.Fatal(err)
		}
		keep[f.Filename] = true
		sums = append(sums, f.Digests.SHA256+"  "+f.Filename)
		fmt.Printf("%s %s\n", f.Filename, f.Digests.SHA256)
	}
	// OUT holds exactly the selected wheels.
	ents, err := os.ReadDir(*out)
	if err != nil {
		log.Fatal(err)
	}
	for _, e := range ents {
		if !keep[e.Name()] && strings.HasSuffix(e.Name(), ".whl") {
			if err := os.Remove(filepath.Join(*out, e.Name())); err != nil {
				log.Fatal(err)
			}
		}
	}
	sort.Strings(sums)
	if err := os.WriteFile(filepath.Join(*out, "SHA256SUMS"), []byte(strings.Join(sums, "\n")+"\n"), 0o644); err != nil {
		log.Fatal(err)
	}
}

func readPins(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var pins []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		if line = strings.TrimSpace(line); line != "" {
			pins = append(pins, line)
		}
	}
	return pins, sc.Err()
}

// target is what a wheel must match: interpreter tags (python, abi) best first, and
// whether a platform tag fits, with its rank (lower is better).
type target struct {
	interp   []string // "cp313-cp313", "cp312-abi3", "py3-none", ...
	platform func(tag string) (rank int, ok bool)
}

func newTarget(python, variant, goosArch, glibc, macos string) (target, error) {
	var t target
	major, minor, ok := strings.Cut(python, ".")
	m, err := strconv.Atoi(minor)
	if !ok || major != "3" || err != nil {
		return t, fmt.Errorf("-python %q: want 3.Y", python)
	}
	cp := "cp3" + minor
	switch variant {
	case "":
		t.interp = append(t.interp, cp+"-"+cp)
		for k := m; k >= 2; k-- { // abi3 wheels built for 3.k load in 3.m for k <= m
			t.interp = append(t.interp, fmt.Sprintf("cp3%d-abi3", k))
		}
	case "freethreaded":
		t.interp = append(t.interp, cp+"-"+cp+"t") // free-threaded builds cannot load abi3
	default:
		return t, fmt.Errorf("-variant %q: want freethreaded or nothing", variant)
	}
	t.interp = append(t.interp, cp+"-none", "py3"+minor+"-none", "py3-none")
	goos, goarch, _ := strings.Cut(goosArch, "/")
	switch goos {
	case "linux":
		arch := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[goarch]
		if arch == "" {
			return t, fmt.Errorf("-target %q: unsupported architecture", goosArch)
		}
		capv, err := parseVersion(glibc)
		if err != nil {
			return t, fmt.Errorf("-glibc: %w", err)
		}
		t.platform = func(tag string) (int, bool) {
			v, ok := manylinuxGlibc(tag, arch)
			if !ok || cmpVersion(v, capv) > 0 {
				return 0, false
			}
			return -(v[0]*1000 + v[1]), true // the newest glibc that fits, as pip prefers
		}
	case "darwin":
		arch := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[goarch]
		if arch == "" {
			return t, fmt.Errorf("-target %q: unsupported architecture", goosArch)
		}
		capv, err := parseVersion(macos)
		if err != nil {
			return t, fmt.Errorf("-macos: %w", err)
		}
		fat := []string{"universal2"}
		if arch == "x86_64" {
			fat = append(fat, "intel", "universal")
		}
		t.platform = func(tag string) (int, bool) {
			rest, ok := strings.CutPrefix(tag, "macosx_")
			if !ok {
				return 0, false
			}
			parts := strings.SplitN(rest, "_", 3)
			if len(parts) != 3 {
				return 0, false
			}
			v, err := parseVersion(parts[0] + "." + parts[1])
			if err != nil || cmpVersion(v, capv) > 0 {
				return 0, false
			}
			if parts[2] == arch {
				return 0, true
			}
			for i, f := range fat {
				if parts[2] == f {
					return 1 + i, true
				}
			}
			return 0, false
		}
	case "windows":
		want := map[string]string{"arm64": "win_arm64", "amd64": "win_amd64"}[goarch]
		if want == "" {
			return t, fmt.Errorf("-target %q: unsupported architecture", goosArch)
		}
		t.platform = func(tag string) (int, bool) { return 0, tag == want }
	default:
		return t, fmt.Errorf("-target %q: want linux, darwin or windows", goosArch)
	}
	return t, nil
}

// manylinuxGlibc returns the glibc version a manylinux platform tag for arch needs.
func manylinuxGlibc(tag, arch string) ([2]int, bool) {
	legacy := map[string][2]int{"manylinux1": {2, 5}, "manylinux2010": {2, 12}, "manylinux2014": {2, 17}}
	base, ok := strings.CutSuffix(tag, "_"+arch)
	if !ok {
		return [2]int{}, false
	}
	if v, ok := legacy[base]; ok {
		return v, true
	}
	rest, ok := strings.CutPrefix(base, "manylinux_")
	if !ok {
		return [2]int{}, false
	}
	v, err := parseVersion(strings.Replace(rest, "_", ".", 1))
	return v, err == nil
}

func parseVersion(s string) ([2]int, error) {
	a, b, ok := strings.Cut(s, ".")
	x, err1 := strconv.Atoi(a)
	y, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil {
		return [2]int{}, fmt.Errorf("%q: want X.Y", s)
	}
	return [2]int{x, y}, nil
}

func cmpVersion(a, b [2]int) int {
	if a[0] != b[0] {
		return a[0] - b[0]
	}
	return a[1] - b[1]
}

// pypiFile is one file of a release in PyPI's JSON API.
type pypiFile struct {
	Filename    string `json:"filename"`
	URL         string `json:"url"`
	PackageType string `json:"packagetype"`
	Yanked      bool   `json:"yanked"`
	Digests     struct {
		SHA256 string `json:"sha256"`
	} `json:"digests"`
}

// rank returns how well a wheel file name fits t: lower is better.
func (t target) rank(filename string) (int, bool) {
	base, ok := strings.CutSuffix(filename, ".whl")
	if !ok {
		return 0, false
	}
	parts := strings.Split(base, "-")
	if len(parts) != 5 && len(parts) != 6 {
		return 0, false
	}
	n := len(parts)
	pys, abis, plats := strings.Split(parts[n-3], "."), strings.Split(parts[n-2], "."), strings.Split(parts[n-1], ".")
	best, found := 0, false
	for i, want := range t.interp {
		wpy, wabi, _ := strings.Cut(want, "-")
		if !contains(pys, wpy) || !contains(abis, wabi) {
			continue
		}
		for _, p := range plats {
			var pr int
			var ok bool
			if p == "any" {
				pr, ok = 100, wabi == "none"
			} else {
				pr, ok = t.platform(p)
			}
			if !ok {
				continue
			}
			if r := i*1_000_000 + pr; !found || r < best {
				best, found = r, true
			}
		}
	}
	return best, found
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var client = &http.Client{Timeout: 5 * time.Minute}

func get(u string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pythago-fetchwheels")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return resp, nil
}

// release returns a release's files, from the cache or PyPI (a pinned release's file
// list only changes when a file is yanked; delete the cache to see that).
func release(cache, name, version string) ([]pypiFile, error) {
	file := filepath.Join(cache, "json", name+"-"+version+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		resp, err := get("https://pypi.org/pypi/" + name + "/" + version + "/json")
		if err != nil {
			return nil, err
		}
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			return nil, err
		}
	}
	var meta struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
		URLs []pypiFile `json:"urls"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if meta.Info.Version != version {
		return nil, fmt.Errorf("%s: PyPI answered for version %q", file, meta.Info.Version)
	}
	return meta.URLs, nil
}

// fetch selects name==version's wheel for t and makes sure the cache holds a verified copy.
func fetch(cache, name, version string, t target) (pypiFile, error) {
	files, err := release(cache, name, version)
	if err != nil {
		return pypiFile{}, err
	}
	var best pypiFile
	bestRank, found := 0, false
	for _, f := range files {
		if f.PackageType != "bdist_wheel" || f.Yanked {
			continue
		}
		if r, ok := t.rank(f.Filename); ok && (!found || r < bestRank) {
			best, bestRank, found = f, r, true
		}
	}
	if !found {
		return pypiFile{}, fmt.Errorf("%s==%s: no wheel for the target (interpreter tags %s)", name, version, strings.Join(t.interp, ", "))
	}
	if len(best.Digests.SHA256) != 64 || strings.ContainsAny(best.Filename, `/\`) {
		return pypiFile{}, fmt.Errorf("%s==%s: bad file entry %q", name, version, best.Filename)
	}
	file := filepath.Join(cache, best.Filename)
	if sum, err := sha256File(file); err == nil && sum == best.Digests.SHA256 {
		return best, nil
	}
	log.Printf("downloading %s", best.URL)
	resp, err := get(best.URL)
	if err != nil {
		return pypiFile{}, err
	}
	defer resp.Body.Close()
	tmp := file + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return pypiFile{}, err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if sum := hex.EncodeToString(h.Sum(nil)); sum != best.Digests.SHA256 {
			err = fmt.Errorf("%s: sha256 %s, PyPI publishes %s", best.URL, sum, best.Digests.SHA256)
		}
	}
	if err != nil {
		os.Remove(tmp)
		return pypiFile{}, err
	}
	return best, os.Rename(tmp, file)
}

// place puts the cached wheel at dst (a hard link when it can), verified again.
func place(src, dst, sum string) error {
	if got, err := sha256File(dst); err == nil && got == sum {
		return nil
	}
	os.Remove(dst)
	if err := os.Link(src, dst); err != nil {
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
	if got, err := sha256File(dst); err != nil || got != sum {
		return errors.Join(fmt.Errorf("%s: does not match its sha256 %s", dst, sum), err)
	}
	return nil
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
