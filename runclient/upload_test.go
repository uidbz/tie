package runclient

// UploadBatch against an in-memory content-addressed fake filehost: the
// composed-tree assembly used to need a real tie-filehost. With the
// fake, the collision rule — two inputs flattening to the same name with
// different content must abort, not silently drop one — is pinned
// end-to-end, and the happy path proves the returned hash is a real
// blob on the filehost.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// blobFilehost is an in-memory content-addressed filehost: PUT stores
// the body under its computed address and echoes the address back —
// exactly what putlib's UploadMultipart and TreeBuilder.uploadBlob
// check. A mismatch between the URL hash and the computed address is
// echoed instead of stored, mirroring a rejecting filehost.
type blobFilehost struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func newBlobFilehost(t *testing.T) (*blobFilehost, string) {
	t.Helper()
	f := &blobFilehost{blobs: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "want PUT", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		addr, err := metadata.HashReader(strings.NewReader(string(body)))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if want := strings.TrimPrefix(r.URL.Path, "/upload/"); want != addr {
			// Reject, but echo the computed address like a real filehost's
			// mismatch reply would surface through StatusItem.Hash.
			w.Write([]byte(addr))
			return
		}
		f.mu.Lock()
		f.blobs[addr] = body
		f.mu.Unlock()
		io.WriteString(w, addr)
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStatForUpload pins the input classification: only a missing path
// may be skipped; permission/IO errors must abort with the path named.
func TestStatForUpload(t *testing.T) {
	dir := t.TempDir()

	// Existing file: nil.
	f := filepath.Join(dir, "present.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := statForUpload(f); err != nil {
		t.Errorf("existing file: got %v, want nil", err)
	}

	// Missing path: the errNotExists sentinel (skip).
	missing := filepath.Join(dir, "nope.txt")
	if err := statForUpload(missing); !errors.Is(err, errNotExists) {
		t.Errorf("missing path: got %v, want errNotExists", err)
	}

	// Permission denied: a wrapped error naming the path, NOT
	// errNotExists. Skipped when running as root (mode bits do not deny
	// root).
	if os.Geteuid() != 0 {
		denied := filepath.Join(dir, "denied")
		if err := os.Mkdir(denied, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(denied, 0o755) })
		if err := statForUpload(filepath.Join(denied, "inside")); err == nil {
			t.Error("denied path: got nil, want error")
		} else if errors.Is(err, errNotExists) {
			t.Errorf("EACCES must not classify as a skippable miss: %v", err)
		} else if !strings.Contains(err.Error(), denied) {
			t.Errorf("denied path error %q must name the path", err)
		}
	}
}

// Two files with the same base name in different directories flatten to
// one name under oneLevel; differing content must abort with a collision
// error naming the path, not upload a tree that lost a file.
func TestUploadBatchCollisionAborts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "data.txt"), "content from a")
	writeFile(t, filepath.Join(dir, "b", "data.txt"), "content from b")

	_, url := newBlobFilehost(t)
	_, err := UploadBatch(url, []string{
		filepath.Join(dir, "a", "data.txt"),
		filepath.Join(dir, "b", "data.txt"),
	}, true, "")
	if err == nil {
		t.Fatal("same-name different-content batch: got nil error, want collision")
	}
	if !strings.Contains(err.Error(), "data.txt") {
		t.Errorf("collision error %q must name the colliding path", err)
	}
}

// Happy path: distinct names upload, the returned root hash resolves as
// a blob on the filehost, and a simply-missing input is skipped rather
// than failing the batch (the optional-input behavior pinned by
// statForUpload).
func TestUploadBatchComposesResolvableTree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "one.txt"), "one")
	writeFile(t, filepath.Join(dir, "two.txt"), "two")

	f, url := newBlobFilehost(t)
	hash, err := UploadBatch(url, []string{
		filepath.Join(dir, "one.txt"),
		filepath.Join(dir, "two.txt"),
		filepath.Join(dir, "missing.txt"), // absent: skipped, not fatal
	}, true, "")
	if err != nil {
		t.Fatalf("UploadBatch: %v", err)
	}
	f.mu.Lock()
	manifest, ok := f.blobs[hash]
	f.mu.Unlock()
	if !ok {
		t.Fatalf("returned root hash %s is not a blob on the filehost", hash)
	}
	if !strings.Contains(string(manifest), "one.txt") || !strings.Contains(string(manifest), "two.txt") {
		t.Errorf("root manifest %q must list both uploaded files", manifest)
	}
}
