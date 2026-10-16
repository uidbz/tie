package tiesfs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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

func newTestCache(t *testing.T, srvURL string, sizeGB int, verify bool) *Cache {
	t.Helper()
	host := client.FileHost{URL: srvURL}
	return NewCache(host.URL, client.HTTPClientFor(host), int64(sizeGB)*1024*1024*1024, verify)
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

	c := newTestCache(t, srv.URL, 1, true)
	defer c.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := c.Blob(hash)
			if err != nil {
				t.Errorf("Blob: %v", err)
				return
			}
			if e.Size() != int64(len(blob)) {
				t.Errorf("size: got %d want %d", e.Size(), len(blob))
			}
			dest := make([]byte, 16)
			if _, err := e.ReadAt(dest, 0); err != nil {
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
// budget still downloads and reads end to end - the disk-backed cache is bounded
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

	c := newTestCache(t, srv.URL, 0, true) // zero-GB budget: everything exceeds it
	defer c.Close()

	e, err := c.Blob(hash)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(blob))
	for off := 0; off < len(blob); {
		dest := make([]byte, 64*1024)
		n, err := e.ReadAt(dest, int64(off))
		if err != nil && err != io.EOF {
			t.Fatalf("ReadAt at %d: %v", off, err)
		}
		if n == 0 {
			t.Fatalf("short read at offset %d", off)
		}
		copy(got[off:], dest[:n])
		off += n
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
	off := newTestCache(t, srv.URL, 1, false)
	defer off.Close()
	if _, err := off.Blob(wanted); err != nil {
		t.Errorf("verify off: expected bytes served without error, got %v", err)
	}

	// verify on: the mismatch is caught.
	on := newTestCache(t, srv.URL, 1, true)
	defer on.Close()
	if _, err := on.Blob(wanted); err == nil {
		t.Error("verify on: expected a hash-mismatch error, got nil")
	}
}
