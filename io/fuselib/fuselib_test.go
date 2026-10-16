package fuselib

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/metadata"
)

// hashOf returns the tie content address of s, matching what a filehost stores.
func hashOf(t *testing.T, s string) string {
	t.Helper()
	h, err := metadata.HashReader(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestListContentClassifiesEntries(t *testing.T) {
	// A nested directory blob and a regular file (PNG magic in head). Hashes
	// are the real content addresses so download verification passes.
	subdir := metadata.DirHeader
	fileBlob := "\x89PNG\r\nsome file contents"

	fileHash := hashOf(t, fileBlob)
	dirHash := hashOf(t, subdir)

	root := metadata.DirHeader
	root += metadata.DirEntry{Hash: fileHash, Filename: "pic.png", Size: len(fileBlob), Head: []byte(fileBlob[:8])}.Line()
	root += metadata.DirEntry{Hash: dirHash, Filename: "sub", Size: len(subdir), Head: []byte(metadata.DirHeader)}.Line()
	rootHash := hashOf(t, root)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case rootHash:
			w.Write([]byte(root))
		case dirHash:
			w.Write([]byte(subdir))
		case fileHash:
			w.Write([]byte(fileBlob))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	state := NewTieFuse(client.FileHost{URL: srv.URL}, 1, true)
	defer state.Close() // release the on-disk cache dir
	n, err := state.listContent(rootHash)
	if err != nil {
		t.Fatal(err)
	}
	if n.Mode != fuse.S_IFDIR {
		t.Fatal("root should be a directory")
	}
	if len(n.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(n.Children))
	}

	byName := map[string]*node{}
	for _, c := range n.Children {
		byName[c.Name] = c
	}
	if byName["pic.png"].Mode != fuse.S_IFREG {
		t.Error("pic.png should be classified as a regular file")
	}
	if byName["pic.png"].Size != len(fileBlob) {
		t.Errorf("pic.png size: got %d want %d", byName["pic.png"].Size, len(fileBlob))
	}
	if byName["sub"].Mode != fuse.S_IFDIR {
		t.Error("sub should be classified as a directory")
	}
}
