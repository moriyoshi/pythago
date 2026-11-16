package py_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/py"
)

// Processes started by the image cache tests run with Config.ImageCacheDir set to
// PYTHAGO_TEST_IMAGE_CACHE_DIR and, with PYTHAGO_TEST_IMAGE_ID, register the test image
// under that id, as another version's image.
func init() {
	dir := os.Getenv("PYTHAGO_TEST_IMAGE_CACHE_DIR")
	if dir == "" || !py.IsPythonCommand() {
		return
	}
	if id := os.Getenv("PYTHAGO_TEST_IMAGE_ID"); id != "" {
		py.SetImageIDForTest(id)
	}
	cfg := testConfig()
	cfg.ImageCacheDir = dir
	os.Exit(py.Main(cfg, os.Args))
}

// imageChild returns a command running this test binary as Python with image cache dir
// and, unless empty, image id.
func imageChild(t *testing.T, ctx context.Context, dir, id string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = append(os.Environ(), "PYTHAGO_PYTHON_EXECUTABLE="+exe,
		"PYTHAGO_TEST_IMAGE_CACHE_DIR="+dir, "PYTHAGO_TEST_IMAGE_ID="+id)
	return cmd
}

// runImageChild runs Python code in a process with image cache dir and id, and returns
// its output.
func runImageChild(t *testing.T, dir, id, code string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := imageChild(t, ctx, dir, id, "-c", code).CombinedOutput()
	if err != nil {
		t.Fatalf("child (image id %q): %v\n%s", id, err, out)
	}
	return string(out)
}

func imageID(t *testing.T) string {
	id := py.ImageIDForTest()
	if id == "" {
		t.Skip("no registered Windows image")
	}
	return id
}

// listDir returns dir's entry names, sorted.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// snapshot lists every file under dir with its size.
func snapshot(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	files := map[string]int64{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files[rel] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	return files
}

// TestImageCacheDir: Config.ImageCacheDir is where the image is extracted, and the
// image's extension modules load from there.
func TestImageCacheDir(t *testing.T) {
	id := imageID(t)
	dir := t.TempDir()
	out := runImageChild(t, dir, "", "import _socket, sys; print(_socket.__file__); print(sys.path)")
	want := []string{"image-" + id, "image-" + id + ".lock"}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Errorf("cache dir holds %q, want %q", got, want)
	}
	file := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if !strings.EqualFold(filepath.Dir(filepath.Dir(file)), filepath.Join(dir, "image-"+id)) {
		t.Errorf("_socket loaded from %s, want under %s", file, filepath.Join(dir, "image-"+id))
	}
}

// TestImageCacheRemovesStale: Init removes images of other ids that no process uses, old
// extraction directories (another id's or its own), and trash; it keeps recent
// extraction directories.
func TestImageCacheRemovesStale(t *testing.T) {
	id := imageID(t)
	dir := t.TempDir()
	runImageChild(t, dir, "stale0", "import _socket")
	if got, want := listDir(t, dir), []string{"image-stale0", "image-stale0.lock"}; !slices.Equal(got, want) {
		t.Fatalf("after the stale0 process: %q, want %q", got, want)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, name := range []string{"tmp-old1-0011223344556677", "tmp-" + id + "-8899aabbccddeeff",
		"tmp-recent1-0123456789abcdef", "trash-0123456789abcdef"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(p, "DLLs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "DLLs", "x.pyd"), []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(name, "recent") {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	runImageChild(t, dir, "", "pass")
	want := []string{"image-" + id, "image-" + id + ".lock", "tmp-recent1-0123456789abcdef"}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Errorf("after a process of image %s: %q, want %q", id, got, want)
	}
}

// TestImageCacheKeepsImageInUse: an image that a running process uses survives another
// image's cleanup whole, and the process can still import extension modules from it;
// once that process has exited, the next cleanup removes it.
func TestImageCacheKeepsImageInUse(t *testing.T) {
	imageID(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	holder := imageChild(t, ctx, dir, "inuse0", "-c", strings.Join([]string{
		"import sys",
		"before = sorted(m for m in ('_sqlite3', 'unicodedata', 'pyexpat', '_ctypes') if m in sys.modules)",
		"print('ready', before, flush=True)",
		"sys.stdin.readline()",
		"import _sqlite3, unicodedata, pyexpat, _ctypes",
		"print('imported', unicodedata.__file__, flush=True)",
	}, "\n"))
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	holder.Stderr = &stderr
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	if !lines.Scan() || !strings.HasPrefix(lines.Text(), "ready") {
		holder.Process.Kill()
		holder.Wait()
		t.Fatalf("holder did not start: %q %v\n%s", lines.Text(), lines.Err(), stderr.String())
	}
	t.Logf("holder: %s", lines.Text())
	image := filepath.Join(dir, "image-inuse0")
	before := snapshot(t, image)

	runImageChild(t, dir, "", "pass") // cleans up, image inuse0 in use

	after := map[string]int64{}
	if _, err := os.Stat(image); err == nil {
		after = snapshot(t, image)
	}
	if fmt.Sprint(after) != fmt.Sprint(before) {
		var missing []string
		for f := range before {
			if _, ok := after[f]; !ok {
				missing = append(missing, f)
			}
		}
		slices.Sort(missing)
		t.Errorf("image inuse0 changed while a process used it: %d of %d files left, missing %q; cache dir %q",
			len(after), len(before), missing, listDir(t, dir))
	}
	io.WriteString(stdin, "go\n")
	stdin.Close()
	if !lines.Scan() || !strings.HasPrefix(lines.Text(), "imported") {
		t.Errorf("holder could not import from its image after the cleanup: %q\n%s", lines.Text(), stderr.String())
	}
	io.Copy(io.Discard, stdout)
	if err := holder.Wait(); err != nil {
		t.Errorf("holder: %v\n%s", err, stderr.String())
	}
	if t.Failed() {
		return
	}

	runImageChild(t, dir, "", "pass") // image inuse0 no longer in use
	if _, err := os.Stat(image); err == nil {
		t.Errorf("image inuse0 left after its process exited: %q", listDir(t, dir))
	}
}

// TestImageCacheConcurrentStarts: processes of two images start together, repeatedly, each
// removing the other's image when it is unused; every process must find a complete image.
func TestImageCacheConcurrentStarts(t *testing.T) {
	imageID(t)
	dir := t.TempDir()
	const code = "import _socket, _sqlite3, unicodedata, pyexpat; print('ok')"
	for round := range 4 {
		var wg sync.WaitGroup
		errs := make(chan string, 8)
		for i := range 6 {
			id := ""
			if i%2 == 1 {
				id = "race0"
			}
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				out, err := imageChild(t, ctx, dir, id, "-c", code).CombinedOutput()
				if err != nil || strings.TrimSpace(string(out)) != "ok" {
					errs <- fmt.Sprintf("round %d, image id %q: %v\n%s", round, id, err, out)
				}
			})
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Error(e)
		}
		if t.Failed() {
			return
		}
	}
	for _, name := range listDir(t, dir) {
		if strings.HasPrefix(name, "tmp-") || strings.HasPrefix(name, "trash-") {
			t.Errorf("left behind: %s (cache dir %q)", name, listDir(t, dir))
		}
	}
}
