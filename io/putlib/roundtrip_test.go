package putlib_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/uidbz/tie/io/getlib"
	"github.com/uidbz/tie/io/putlib"
)

// blobStore is an in-memory content-addressed filehost: PUT /upload/<hash>
// stores the body and echoes the hash; GET /<hash> returns the stored bytes.
type blobStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func newBlobStore() *blobStore { return &blobStore{blobs: map[string][]byte{}} }

func (s *blobStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		hash := strings.TrimPrefix(r.URL.Path, "/upload/")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.blobs[hash] = body
		s.mu.Unlock()
		w.Write([]byte(hash))
	case http.MethodGet:
		hash := strings.TrimPrefix(r.URL.Path, "/")
		s.mu.Lock()
		body, ok := s.blobs[hash]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"a.txt":          "alpha",
		"sub/b.txt":      "bravo",
		"sub/deep/c.txt": "charlie",
	}
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRoundTripPathInvariance is the load-bearing guarantee of the v2 manifest:
// a directory's hash depends only on its contents, not on the path used to
// upload it, and checkout composes paths from stored basenames under the dest.
func TestRoundTripPathInvariance(t *testing.T) {
	srv := httptest.NewServer(newBlobStore())
	defer srv.Close()

	base := t.TempDir()
	treeDir := filepath.Join(base, "mytree")
	writeTree(t, treeDir)

	// Run from base so relative and dot-relative paths resolve to the tree.
	defer chdir(t, base)()

	// The same tree referenced three different ways must hash identically.
	invocations := []struct {
		name string
		arg  string
	}{
		{"relative", "mytree"},
		{"dot-relative", "./mytree"},
		{"absolute", treeDir},
	}

	var rootHash string
	for _, inv := range invocations {
		status := putlib.Upload(srv.URL, inv.arg, putlib.PutConfig{})
		if status.ErrorMsg != "" {
			t.Fatalf("%s: upload error: %s", inv.name, status.ErrorMsg)
		}
		h := status.LastItem.Hash
		if h == "" {
			t.Fatalf("%s: empty root hash", inv.name)
		}
		if rootHash == "" {
			rootHash = h
		} else if h != rootHash {
			t.Fatalf("%s: root hash %s differs from %s — hash is not path-invariant", inv.name, h, rootHash)
		}
	}

	// Check out the tree and assert the layout is rebuilt relative to dest,
	// independent of how it was uploaded.
	dest := filepath.Join(base, "checkout")
	if err := getlib.DownloadFile(nil, srv.URL, rootHash, dest, nil); err != nil {
		t.Fatalf("download: %v", err)
	}

	want := map[string]string{
		"a.txt":          "alpha",
		"sub/b.txt":      "bravo",
		"sub/deep/c.txt": "charlie",
	}
	var got []string
	err := filepath.Walk(dest, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dest, path)
		rel = filepath.ToSlash(rel)
		got = append(got, rel)
		content, _ := os.ReadFile(path)
		if want[rel] != string(content) {
			t.Errorf("%s: content = %q, want %q", rel, content, want[rel])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	wantPaths := []string{"a.txt", "sub/b.txt", "sub/deep/c.txt"}
	if strings.Join(got, ",") != strings.Join(wantPaths, ",") {
		t.Errorf("checkout layout = %v, want %v", got, wantPaths)
	}
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { os.Chdir(prev) }
}
