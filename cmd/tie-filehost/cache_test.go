package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/tie/auth"
)

// newCachedTestServer builds a single-store filehost whose reads go through a
// read cache rooted at cacheDir.
func newCachedTestServer(t *testing.T, cacheDir string, maxBytes int64) *testHost {
	t.Helper()
	dir := t.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "default", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	c, err := newBlobCache(cacheDir, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	srv.cache = c
	mux := http.NewServeMux()
	srv.routes(mux, auth.NewStore(nil, auth.RoleWrite))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return &testHost{s: srv, hs: hs, st: srv.storeByName("default")}
}

// upload puts body in the default store and returns its hash.
func upload(t *testing.T, h *testHost, body string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %s", resp.Status)
	}
	return strings.TrimSpace(string(b))
}

// getBlob downloads hash and returns its body.
func getBlob(t *testing.T, h *testHost, hash string) (int, string) {
	t.Helper()
	resp, err := http.Get(h.hs.URL + "/" + hash)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestReadCacheFillsBehindTheResponse is the §4.16 copy-before-serve guard: a
// cold GET must be answered from the store without waiting for the cache copy,
// and the copy must land afterwards.
func TestReadCacheFillsBehindTheResponse(t *testing.T) {
	h := newCachedTestServer(t, t.TempDir(), 1<<20)
	body := "cached blob content"
	hash := upload(t, h, body)

	code, got := getBlob(t, h, hash)
	if code != http.StatusOK || got != body {
		t.Fatalf("cold GET = (%d, %q)", code, got)
	}
	// The response was served from the store; the fill runs behind it.
	waitFor(t, "the cache fill to publish the entry", func() bool {
		_, ok := h.s.cache.peek(hash)
		return ok
	})
	// A warm GET is served through the cache handle and must be identical.
	hh, ok := h.s.cache.peek(hash)
	if !ok {
		t.Fatal("entry vanished")
	}
	out := make([]byte, hh.Size())
	if _, err := hh.ReadAt(out, 0); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	hh.Close()
	if string(out) != body {
		t.Errorf("cached copy = %q, want %q", out, body)
	}
	if code, got := getBlob(t, h, hash); code != http.StatusOK || got != body {
		t.Errorf("warm GET = (%d, %q)", code, got)
	}
}

// TestReadCacheAdoptsAcrossRestart: the CachePath directory is reused, so a
// new process must count and serve what the previous one left behind. Before
// this the entry map started empty every run and the directory grew forever.
func TestReadCacheAdoptsAcrossRestart(t *testing.T) {
	cacheDir := t.TempDir()
	h := newCachedTestServer(t, cacheDir, 1<<20)
	body := "survives a restart"
	hash := upload(t, h, body)
	if code, _ := getBlob(t, h, hash); code != http.StatusOK {
		t.Fatalf("cold GET: %d", code)
	}
	waitFor(t, "the fill", func() bool { _, ok := h.s.cache.peek(hash); return ok })
	h.s.cache.Close()

	// A second filehost over the same cache directory.
	h2 := newCachedTestServer(t, cacheDir, 1<<20)
	hh, ok := h2.s.cache.peek(hash)
	if !ok {
		t.Fatal("the restarted cache does not know the blob its predecessor cached")
	}
	out := make([]byte, hh.Size())
	if _, err := hh.ReadAt(out, 0); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	hh.Close()
	if string(out) != body {
		t.Errorf("adopted copy = %q, want %q", out, body)
	}
	if bytes, entries := h2.s.cache.dc.Stats(); entries != 1 || bytes != int64(len(body)) {
		t.Errorf("adopted stats = (%d bytes, %d entries); the budget must count what a previous run left", bytes, entries)
	}
}

// TestReadCacheBudgetTrimmedAtAdopt: a cache directory left by a run with a
// larger budget must come back inside the current one at start-up, dropping the
// least recently used files first.
func TestReadCacheBudgetTrimmedAtAdopt(t *testing.T) {
	cacheDir := t.TempDir()
	old, newer := hexHash("aa"), hexHash("bb")
	write := func(hash string, age time.Duration) {
		p, err := PathFromHash(cacheDir, hash)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Repeat("x", 60)), 0600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, time.Now().Add(-age), time.Now().Add(-age))
	}
	write(old, time.Hour)
	write(newer, time.Minute)

	c, err := newBlobCache(cacheDir, 100) // fits one 60-byte blob
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, ok := c.peek(newer); !ok {
		t.Error("the most recently used cached blob was dropped at start-up")
	}
	if _, ok := c.peek(old); ok {
		t.Error("the stale cached blob survived a budget it no longer fits")
	}
	if p, _ := PathFromHash(cacheDir, old); fileExists(p) {
		t.Error("the evicted cache file was left on disk")
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestReadCacheServesThroughHandleAfterEviction: serving by path raced eviction
// (the file is unlinked, the request 404s). Serving through the handle's fd
// cannot.
func TestReadCacheServesThroughHandleAfterEviction(t *testing.T) {
	cacheDir := t.TempDir()
	h := newCachedTestServer(t, cacheDir, 120) // fits one ~60-byte blob
	a := upload(t, h, strings.Repeat("A", 60))
	b := upload(t, h, strings.Repeat("B", 60))

	if code, got := getBlob(t, h, a); code != http.StatusOK || got != strings.Repeat("A", 60) {
		t.Fatalf("GET a = (%d, %d bytes)", code, len(got))
	}
	waitFor(t, "a to be cached", func() bool { _, ok := h.s.cache.peek(a); return ok })

	// Admit b, which evicts a.
	if code, _ := getBlob(t, h, b); code != http.StatusOK {
		t.Fatalf("GET b: %d", code)
	}
	waitFor(t, "b to be cached", func() bool { _, ok := h.s.cache.peek(b); return ok })

	// a is gone from the cache but still in the store: the download must still
	// work, served from the store while the cache refills.
	if code, got := getBlob(t, h, a); code != http.StatusOK || got != strings.Repeat("A", 60) {
		t.Errorf("GET after eviction = (%d, %d bytes), want the blob", code, len(got))
	}
}
