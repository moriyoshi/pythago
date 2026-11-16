package testwheels

import (
	"archive/zip"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type entry struct {
	name string
	mode fs.FileMode
	data string
}

func writeWheel(t *testing.T, file string, entries ...entry) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e.data))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestExtract: files land under the directory with their contents and execute bits,
// the library directory beside the package included.
func TestExtract(t *testing.T) {
	whl := filepath.Join(t.TempDir(), "pkg-1.0-cp313-cp313-linux_aarch64.whl")
	writeWheel(t, whl,
		entry{"pkg/__init__.py", 0o644, "x = 1\n"},
		entry{"pkg/_native.so", 0o755, "ELF"},
		entry{"pkg.libs/", fs.ModeDir | 0o755, ""},
		entry{"pkg.libs/libdep-abc.so.1", 0o755, "dep"},
		entry{"pkg-1.0.dist-info/RECORD", 0o644, ""})
	dir := t.TempDir()
	if err := extract(whl, dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"pkg/__init__.py": "x = 1\n", "pkg/_native.so": "ELF", "pkg.libs/libdep-abc.so.1": "dep"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]fs.FileMode{"pkg/_native.so": 0o755, "pkg.libs/libdep-abc.so.1": 0o755, "pkg/__init__.py": 0o644} {
			st, err := os.Stat(filepath.Join(dir, name))
			if err != nil || st.Mode().Perm()&0o111 != want&0o111 {
				t.Errorf("%s: mode %v %v, want %v", name, st.Mode(), err, want)
			}
		}
	}
}

// TestExtractRejects: entries outside the directory, symbolic links and duplicates are
// errors, and nothing is written outside the directory.
func TestExtractRejects(t *testing.T) {
	for _, tc := range []struct {
		e    []entry
		want string
	}{
		{[]entry{{"../escape.py", 0o644, "x"}}, "leaves the directory"},
		{[]entry{{"/abs/escape.py", 0o644, "x"}}, "leaves the directory"},
		{[]entry{{"pkg/../../escape.py", 0o644, "x"}}, "leaves the directory"},
		{[]entry{{"pkg/link", fs.ModeSymlink | 0o777, "/etc/passwd"}}, "symbolic link"},
		{[]entry{{"pkg/a.py", 0o644, "1"}, {"pkg/a.py", 0o644, "2"}}, "exists"},
	} {
		root := t.TempDir()
		whl := filepath.Join(root, "bad-1.0-py3-none-any.whl")
		writeWheel(t, whl, tc.e...)
		dir := filepath.Join(root, "site")
		os.Mkdir(dir, 0o755)
		err := extract(whl, dir)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want an error with %q", tc.e[len(tc.e)-1].name, err, tc.want)
		}
		if _, err := os.Stat(filepath.Join(root, "escape.py")); err == nil {
			t.Errorf("%v: wrote outside the directory", tc.e[0].name)
		}
	}
}

// TestSiteUnset: without PYTHAGO_TEST_WHEELS there is no site and no error.
func TestSiteUnset(t *testing.T) {
	t.Setenv(Env, "")
	if dir, files, err := extractAll(); dir != "" || files != nil || err != nil {
		t.Fatalf("%q %v %v", dir, files, err)
	}
	t.Setenv(Env, t.TempDir())
	if _, _, err := extractAll(); err == nil || !strings.Contains(err.Error(), "no wheels") {
		t.Fatalf("an empty directory: %v", err)
	}
}
