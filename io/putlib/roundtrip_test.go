package putlib_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	// puts and heads count request kinds, so a test can assert a re-import
	// asks instead of re-sending.
	puts  int
	heads int
}

func newBlobStore() *blobStore { return &blobStore{blobs: map[string][]byte{}} }

func (s *blobStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		hash := strings.TrimPrefix(r.URL.Path, "/upload/")
		s.mu.Lock()
		s.puts++
		s.mu.Unlock()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.blobs[hash] = body
		s.mu.Unlock()
		w.Write([]byte(hash))
	case http.MethodHead:
		hash := strings.TrimPrefix(r.URL.Path, "/")
		s.mu.Lock()
		s.heads++
		body, ok := s.blobs[hash]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
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

// TestReimportSkipsExistingBlobs is the §5.2(2) guard: putlib used to stream
// every byte again even when the filehost already held the content address, so
// re-importing a library moved the whole library across the wire for nothing.
func TestReimportSkipsExistingBlobs(t *testing.T) {
	store := newBlobStore()
	srv := httptest.NewServer(store)
	defer srv.Close()

	base := t.TempDir()
	writeTree(t, filepath.Join(base, "mytree"))
	defer chdir(t, base)()

	first := putlib.Upload(srv.URL, "mytree", putlib.PutConfig{})
	if first.ErrorMsg != "" {
		t.Fatalf("first import: %s", first.ErrorMsg)
	}
	store.mu.Lock()
	putsAfterFirst := store.puts
	store.mu.Unlock()
	if putsAfterFirst == 0 {
		t.Fatal("the first import stored nothing")
	}

	second := putlib.Upload(srv.URL, "mytree", putlib.PutConfig{})
	if second.ErrorMsg != "" {
		t.Fatalf("second import: %s", second.ErrorMsg)
	}
	store.mu.Lock()
	extraPuts := store.puts - putsAfterFirst
	heads := store.heads
	store.mu.Unlock()

	if extraPuts != 0 {
		t.Errorf("re-import sent %d PUTs, want 0", extraPuts)
	}
	if heads == 0 {
		t.Error("the re-import asked no HEAD requests")
	}
	if second.LastItem.Hash != first.LastItem.Hash {
		t.Errorf("root hash changed: %s vs %s", second.LastItem.Hash, first.LastItem.Hash)
	}
	// The skipped items must still carry the metadata callers rely on
	// (tagging, manifest entries, media type).
	for _, it := range second.UploadedItems {
		if it.Hash == "" {
			t.Errorf("item %q has no hash", it.Filename)
		}
		if it.Size == 0 {
			t.Errorf("item %q has no size", it.Filename)
		}
	}
}

// BenchmarkReimport measures the cost of re-importing an unchanged tree: the
// HEAD probe replaces a full read-and-send of every blob.
func BenchmarkReimport(b *testing.B) {
	store := newBlobStore()
	srv := httptest.NewServer(store)
	defer srv.Close()

	base := b.TempDir()
	tree := filepath.Join(base, "mytree")
	for i := 0; i < 20; i++ {
		rel := filepath.Join("sub", strconv.Itoa(i)+".bin")
		full := filepath.Join(tree, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(full, bytes.Repeat([]byte("x"), 32*1024), 0644); err != nil {
			b.Fatal(err)
		}
	}
	prev, err := os.Getwd()
	if err != nil {
		b.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		b.Fatal(err)
	}
	defer os.Chdir(prev)

	if st := putlib.Upload(srv.URL, "mytree", putlib.PutConfig{}); st.ErrorMsg != "" {
		b.Fatal(st.ErrorMsg)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if st := putlib.Upload(srv.URL, "mytree", putlib.PutConfig{}); st.ErrorMsg != "" {
			b.Fatal(st.ErrorMsg)
		}
	}
}

// TestUploadDoesNotChdir: the library used to offer a PathToWorkdir option that
// called os.Chdir around an upload. That is process-global — it moves the
// working directory out from under every other goroutine, which is a data race
// in the making once the library runs inside FUSE or SMB (§5.1 L14). Nothing
// ever set the option, and it is gone; this test keeps it gone.
//
// The old code deferred the restore, so comparing the working directory before
// and after would pass anyway. What matters is what it is *during* the upload,
// so the server handler — same process, other goroutine — reports it.
func TestUploadDoesNotChdir(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	store := newBlobStore()
	var (
		mu     sync.Mutex
		seen   []string
		inside = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wd, err := os.Getwd()
			if err == nil {
				mu.Lock()
				seen = append(seen, wd)
				mu.Unlock()
			}
			store.ServeHTTP(w, r)
		}))
	)
	defer inside.Close()

	base := t.TempDir()
	treeDir := filepath.Join(base, "mytree")
	writeTree(t, treeDir)

	status := putlib.Upload(inside.URL, treeDir, putlib.PutConfig{})
	if status.ErrorMsg != "" {
		t.Fatalf("upload failed: %s", status.ErrorMsg)
	}

	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("Upload left the process working directory at %s, was %s", after, before)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the upload never reached the server")
	}
	for _, wd := range seen {
		if wd != before {
			t.Errorf("during the upload the process working directory was %s, not %s", wd, before)
			break
		}
	}
}
