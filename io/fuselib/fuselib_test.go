package fuselib

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.sr.ht/~uid/tie/metadata"
	"github.com/hanwen/go-fuse/v2/fuse"
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

	state := NewTieFuse(srv.URL, false, 1)
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

// TestCacheConcurrentAccess drives the cache from many goroutines so the race
// detector can catch unsynchronized access to Entries/CurrentSize.
func TestCacheConcurrentAccess(t *testing.T) {
	c := &cache{MaxSize: 1024}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i%16))
			c.set(key, []byte(strings.Repeat("x", 8)))
			c.get(key)
		}(i)
	}
	wg.Wait()
}
