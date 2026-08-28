package fuselib

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/metadata"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func TestParseTagQuery(t *testing.T) {
	tests := []struct {
		query     string
		wantScope string
		wantInc   []string
		wantExc   []string
	}{
		{"jazz", "", []string{"jazz"}, nil},
		{"jazz mellow -live", "", []string{"jazz", "mellow"}, []string{"live"}},
		{"mellow type:audio", "audio-file", []string{"mellow"}, nil},
		{"type:image", "image-file", nil, nil},
		{"  jazz   -live  ", "", []string{"jazz"}, []string{"live"}},
		{"type:live-album", "live-album", nil, nil},                 // custom label kept literally
		{"type:audio-dir jazz", "audio-dir", []string{"jazz"}, nil}, // full type name passes through
		{"-", "", nil, nil}, // bare dash ignored
	}
	for _, tt := range tests {
		gotScope, gotInc, gotExc := parseTagQuery(tt.query)
		if gotScope != tt.wantScope {
			t.Errorf("parseTagQuery(%q) scope = %q, want %q", tt.query, gotScope, tt.wantScope)
		}
		if !reflect.DeepEqual(gotInc, tt.wantInc) {
			t.Errorf("parseTagQuery(%q) include = %v, want %v", tt.query, gotInc, tt.wantInc)
		}
		if !reflect.DeepEqual(gotExc, tt.wantExc) {
			t.Errorf("parseTagQuery(%q) exclude = %v, want %v", tt.query, gotExc, tt.wantExc)
		}
	}
}

func TestDisambiguate(t *testing.T) {
	in := []client.TaggedFile{
		{Hash: "5075cc466d7a3f83aaaaaaaaaaaaaaaa", Filename: "SomeDir", IsDir: true},
		{Hash: "42e55dcfce802245bbbbbbbbbbbbbbbb", Filename: "SomeDir", IsDir: true},
		{Hash: "0f4098fbf4a52ae8cccccccccccccccc", Filename: "track1.jpg"},
		{Hash: "d39f1cac0b7accc6dddddddddddddddd", Filename: "track1.jpg"},
		{Hash: "eeee0000eeee0000eeee0000eeee0000", Filename: "unique.txt"},
	}
	got := disambiguate(in)
	want := []string{
		"SomeDir",             // first keeps the bare name
		"SomeDir~42e55dcf",    // collision suffixed with short hash
		"track1.jpg",          // first keeps the bare name
		"track1~d39f1cac.jpg", // suffix inserted before the extension
		"unique.txt",          // no collision, untouched
	}
	for i, w := range want {
		if got[i].Filename != w {
			t.Errorf("entry %d: got %q, want %q", i, got[i].Filename, w)
		}
	}
	// Every resulting name must be unique so each match is reachable by Lookup.
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f.Filename] {
			t.Errorf("duplicate name after disambiguation: %q", f.Filename)
		}
		seen[f.Filename] = true
	}
}

func TestDisambiguateFiles(t *testing.T) {
	// A _prev history dir can hold several versions of one file, all recorded
	// under the same filename; disambiguateFiles must uniquify them by hash.
	in := []client.File{
		{Uid: "0f4098fbf4a52ae8cccccccccccccccc", Filename: "file1.txt"},
		{Uid: "d39f1cac0b7accc6dddddddddddddddd", Filename: "file1.txt"},
		{Uid: "eeee0000eeee0000eeee0000eeee0000", Filename: "unique.txt"},
	}
	got := disambiguateFiles(in)
	want := []string{
		"file1.txt",          // first keeps the bare name
		"file1~d39f1cac.txt", // collision suffixed before the extension
		"unique.txt",         // no collision, untouched
	}
	for i, w := range want {
		if got[i].Filename != w {
			t.Errorf("entry %d: got %q, want %q", i, got[i].Filename, w)
		}
	}
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f.Filename] {
			t.Errorf("duplicate name after disambiguation: %q", f.Filename)
		}
		seen[f.Filename] = true
	}
}

func TestBaseName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"tie:/music", "music"},
		{"tie:/music/jazz", "jazz"},
		{"tie:/music/jazz/", "jazz"},
		{"tie:/", ""},
		{"music", "music"},
	}
	for _, tt := range tests {
		if got := baseName(tt.in); got != tt.want {
			t.Errorf("baseName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

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
// detector can catch unsynchronized access to the entry map and LRU state, and
// asserts that concurrent reads of one hash coalesce into a single download.
func TestCacheConcurrentAccess(t *testing.T) {
	blob := strings.Repeat("x", 4096)
	hash := hashOf(t, blob)

	var downloads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/") == hash {
			downloads.Add(1)
			w.Write([]byte(blob))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	state := NewTieFuse(client.FileHost{URL: srv.URL}, 1, true)
	defer state.Close()
	c := state.cache

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := c.blob(hash)
			if err != nil {
				t.Errorf("blob: %v", err)
				return
			}
			if e.size != int64(len(blob)) {
				t.Errorf("size: got %d want %d", e.size, len(blob))
			}
			dest := make([]byte, 16)
			if _, err := e.file.ReadAt(dest, 0); err != nil {
				t.Errorf("ReadAt: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := downloads.Load(); got != 1 {
		t.Errorf("expected 1 download (single-flight), got %d", got)
	}
}

// TestCacheServesFileLargerThanBudget verifies a blob bigger than the cache
// budget still downloads and reads end to end — the disk-backed cache is bounded
// by disk, not the in-memory budget, so large files are no longer unreadable.
func TestCacheServesFileLargerThanBudget(t *testing.T) {
	blob := strings.Repeat("abcd", 512*1024) // 2 MiB
	hash := hashOf(t, blob)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/") == hash {
			w.Write([]byte(blob))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	state := NewTieFuse(client.FileHost{URL: srv.URL}, 0, true) // zero-GB budget: everything exceeds it
	defer state.Close()

	fh := &bytesFileHandle{hash: hash, cache: state.cache}
	got := make([]byte, len(blob))
	for off := 0; off < len(blob); {
		dest := make([]byte, 64*1024)
		res, errno := fh.Read(nil, dest, int64(off))
		if errno != 0 {
			t.Fatalf("Read at %d: errno %v", off, errno)
		}
		b, status := res.Bytes(make([]byte, len(dest)))
		if status != fuse.OK {
			t.Fatalf("ReadResult status: %v", status)
		}
		if len(b) == 0 {
			t.Fatalf("short read at offset %d", off)
		}
		copy(got[off:], b)
		off += len(b)
	}
	if string(got) != blob {
		t.Error("read bytes do not match the served blob")
	}
}

// TestCacheVerifyFlag checks that byte verification is gated by the flag: with
// verify off (the default) a filehost that returns the wrong bytes is served
// without error, and with verify on the mismatch is rejected.
func TestCacheVerifyFlag(t *testing.T) {
	wanted := hashOf(t, "the real content")
	// The server returns bytes that do NOT hash to the requested address.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered content"))
	}))
	defer srv.Close()

	// verify off: mismatched bytes are served anyway.
	off := NewTieFuse(client.FileHost{URL: srv.URL}, 1, false)
	defer off.Close()
	if _, err := off.cache.blob(wanted); err != nil {
		t.Errorf("verify off: expected bytes served without error, got %v", err)
	}

	// verify on: the mismatch is caught.
	on := NewTieFuse(client.FileHost{URL: srv.URL}, 1, true)
	defer on.Close()
	if _, err := on.cache.blob(wanted); err == nil {
		t.Error("verify on: expected a hash-mismatch error, got nil")
	}
}
