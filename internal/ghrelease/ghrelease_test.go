package ghrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestToken checks that the API host, and no other, receives the token: a release lookup
// and a redirect within the API host carry it; an asset download from another host, and a
// redirect from the API host to another one, do not. Both servers listen on 127.0.0.1, so
// net/http's own redirect policy (which ignores ports) would forward the header.
func TestToken(t *testing.T) {
	payload := []byte("asset payload")
	sum := sha256.Sum256(payload)

	var mu sync.Mutex
	seen := map[string]string{} // "server path" -> Authorization
	record := func(server string, r *http.Request) {
		mu.Lock()
		seen[server+" "+r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("other", r)
		w.Write(payload)
	}))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("api", r)
		switch r.URL.Path {
		case "/repos/o/r/releases/tags/t":
			json.NewEncoder(w).Encode(map[string]any{"assets": []Asset{
				{Name: "direct", Digest: "sha256:" + hex.EncodeToString(sum[:]), URL: other.URL + "/direct"},
				{Name: "away", Digest: "sha256:" + hex.EncodeToString(sum[:]), URL: "http://" + r.Host + "/away"},
				{Name: "within", Digest: "sha256:" + hex.EncodeToString(sum[:]), URL: "http://" + r.Host + "/within"},
			}})
		case "/away":
			http.Redirect(w, r, other.URL+"/redirected", http.StatusFound)
		case "/within":
			http.Redirect(w, r, "/landed", http.StatusFound)
		default:
			w.Write(payload)
		}
	}))
	defer api.Close()
	defer func(b string) { apiBase = b }(apiBase)
	apiBase = api.URL

	for _, tc := range []struct{ github, gh, want string }{
		{"", "", ""},
		{"", "ghtok", "Bearer ghtok"},
		{"githubtok", "ghtok", "Bearer githubtok"},
	} {
		t.Setenv("GITHUB_TOKEN", tc.github)
		t.Setenv("GH_TOKEN", tc.gh)
		clear(seen)
		rel := Release{Repo: "o/r", Tag: "t", Cache: t.TempDir()}
		assets, err := rel.Assets()
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range assets {
			file, err := rel.Download(a)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(file); string(got) != string(payload) || filepath.Base(file) != a.Name {
				t.Fatalf("%s: got %q", file, got)
			}
		}
		want := map[string]string{
			"api /repos/o/r/releases/tags/t": tc.want,
			"other /direct":                  "",
			"api /away":                      tc.want,
			"other /redirected":              "",
			"api /within":                    tc.want,
			"api /landed":                    tc.want,
		}
		if len(seen) != len(want) {
			t.Errorf("tokens %q/%q: requests %q, want %q", tc.github, tc.gh, seen, want)
		}
		for k, w := range want {
			if got, ok := seen[k]; !ok || got != w {
				t.Errorf("tokens %q/%q: %s: Authorization %q (seen %v), want %q", tc.github, tc.gh, k, got, ok, w)
			}
		}
	}
}
