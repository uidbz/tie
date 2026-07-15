package getlib

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~uid/tie/metadata"
)

// blobServer is an in-memory filehost. Blobs may be stored under a wrong key on
// purpose to exercise verification.
type blobServer struct {
	blobs map[string][]byte
}

func newBlobServer() *blobServer { return &blobServer{blobs: map[string][]byte{}} }

// put stores data under its true content address and returns that hash.
func (b *blobServer) put(t *testing.T, data string) string {
	t.Helper()
	h, err := metadata.HashReader(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b.blobs[h] = []byte(data)
	return h
}

func (b *blobServer) start() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		body, ok := b.blobs[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
}

func TestDownloadFileVerifiesContent(t *testing.T) {
	bs := newBlobServer()
	content := strings.Repeat("hello world ", 5000) // exercise streaming past one buffer
	hash := bs.put(t, content)
	srv := bs.start()
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.txt")
	if err := DownloadFile(nil, srv.URL, hash, dest, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content mismatch: got %d bytes want %d", len(got), len(content))
	}
}

func TestDownloadFileRejectsTamperedContent(t *testing.T) {
	bs := newBlobServer()
	// Store bytes under a hash that does not address them.
	realHash := bs.put(t, "legitimate content")
	tamperedHash := realHash
	bs.blobs[tamperedHash] = []byte("evil replacement bytes")

	srv := bs.start()
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.txt")
	err := DownloadFile(nil, srv.URL, tamperedHash, dest, nil)
	if err != ErrChecksum {
		t.Fatalf("expected ErrChecksum, got %v", err)
	}
}

func TestDownloadFileRejectsInvalidHash(t *testing.T) {
	srv := newBlobServer().start()
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.txt")
	if err := DownloadFile(nil, srv.URL, "../../etc/passwd", dest, nil); err == nil {
		t.Fatal("expected error for non-hex hash")
	}
}

// TestDownloadRejectsCyclicManifest confirms verification alone stops a server
// that maps a hash to a manifest referencing that same hash: the manifest
// cannot hash to the address it is served under, so the download aborts rather
// than looping. (Content addressing makes an honest cycle impossible to build.)
func TestDownloadRejectsCyclicManifest(t *testing.T) {
	bs := newBlobServer()
	selfHash := strings.Repeat("a", 64)
	manifest := metadata.DirHeader +
		metadata.DirEntry{Hash: selfHash, Filename: "loop", Size: 0, Head: []byte(metadata.DirHeader)}.Line()
	bs.blobs[selfHash] = []byte(manifest)

	srv := bs.start()
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	err := DownloadFile(nil, srv.URL, selfHash, dest, nil)
	if err != ErrChecksum {
		t.Fatalf("expected ErrChecksum for cyclic manifest, got %v", err)
	}
}

// TestDownloadDepthLimit builds a genuinely deep but valid directory chain
// (each level a distinct real hash) and confirms traversal aborts at the depth
// cap instead of exhausting the stack.
func TestDownloadDepthLimit(t *testing.T) {
	bs := newBlobServer()

	// Leaf file, then wrap it in nested directories past maxDepth.
	hash := bs.put(t, "leaf contents")
	head := []byte("leaf con")
	for i := 0; i <= maxDepth+1; i++ {
		manifest := metadata.DirHeader +
			metadata.DirEntry{Hash: hash, Filename: "d", Size: 0, Head: head}.Line()
		hash = bs.put(t, manifest)
		head = []byte(metadata.DirHeader) // intermediate levels are directories
	}

	srv := bs.start()
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	err := DownloadFile(nil, srv.URL, hash, dest, nil)
	if err == nil {
		t.Fatal("expected depth-limit error for over-deep tree")
	}
	if !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("expected nesting error, got %v", err)
	}
}
