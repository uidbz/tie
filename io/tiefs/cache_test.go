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

// TestCacheEvictionKeepsLiveHandle is the §3.2 guard from the risk map: read A,
// evict it by reading B, then read A again. The old evictLocked closed the fd of
// the evicted entry, which every live Blob was still reading through — SMB got
// "file already closed", FUSE got EIO.
func TestCacheEvictionKeepsLiveHandle(t *testing.T) {
	a := strings.Repeat("A", 4096)
	b := strings.Repeat("B", 4096)
	ha, hb := hashOf(t, a), hashOf(t, b)

	var downloads sync.Map // hash -> *atomic.Int64
	count := func(h string) *atomic.Int64 {
		v, _ := downloads.LoadOrStore(h, new(atomic.Int64))
		return v.(*atomic.Int64)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := strings.TrimPrefix(r.URL.Path, "/")
		switch h {
		case ha:
			count(ha).Add(1)
			w.Write([]byte(a))
		case hb:
			count(hb).Add(1)
			w.Write([]byte(b))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Budget fits one blob, so admitting B evicts A.
	host := client.FileHost{URL: srv.URL}
	c := NewCache(srv.URL, client.HTTPClientFor(host), 5000, true)
	defer c.Close()

	get := func(h string) *Blob {
		e, err := c.Blob(h)
		if err != nil {
			t.Fatalf("Blob %s: %v", h, err)
		}
		return e
	}
	read := func(e *Blob) string {
		out := make([]byte, e.Size())
		if _, err := e.ReadAt(out, 0); err != nil && err != io.EOF {
			t.Fatalf("ReadAt: %v", err)
		}
		return string(out)
	}

	ea := get(ha)
	if got := read(ea); got != a {
		t.Fatalf("first read of A returned %d bytes", len(got))
	}

	eb := get(hb) // forces A out of the cache
	if n := count(ha).Load(); n != 1 {
		t.Fatalf("A downloaded %d times before eviction", n)
	}

	// The live handle must keep working across the eviction.
	if got := read(ea); got != a {
		t.Errorf("read through A after eviction returned %q, want the blob", firstN(got, 16))
	}

	// Once the handle is released the entry is gone: a fresh lookup re-downloads.
	ea.Close()
	eb.Close()
	ea2 := get(ha)
	defer ea2.Close()
	if n := count(ha).Load(); n != 2 {
		t.Errorf("A downloaded %d times after eviction; the entry should have left the cache", n)
	}
}

// TestCacheEvictionIsLRU: the old order slice was insertion order, so the
// hottest blob was evicted as readily as the coldest. A hit must move the
// entry to the back.
func TestCacheEvictionIsLRU(t *testing.T) {
	const size = 2048
	mk := func(c byte) (string, string) {
		s := strings.Repeat(string(c), size)
		return s, hashOf(t, s)
	}
	_, ha := mk('A')
	_, hb := mk('B')
	_, hc := mk('C')

	var downloads sync.Map
	count := func(h string) *atomic.Int64 {
		v, _ := downloads.LoadOrStore(h, new(atomic.Int64))
		return v.(*atomic.Int64)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := strings.TrimPrefix(r.URL.Path, "/")
		body := map[string]string{ha: strings.Repeat("A", size), hb: strings.Repeat("B", size), hc: strings.Repeat("C", size)}[h]
		if body == "" {
			http.NotFound(w, r)
			return
		}
		count(h).Add(1)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	host := client.FileHost{URL: srv.URL}
	c := NewCache(srv.URL, client.HTTPClientFor(host), 2*size+100, true) // fits two blobs
	defer c.Close()

	get := func(h string) *Blob {
		e, err := c.Blob(h)
		if err != nil {
			t.Fatalf("Blob %s: %v", h, err)
		}
		return e
	}

	ea := get(ha)
	defer ea.Close()
	eb := get(hb)
	defer eb.Close()

	// Look A up again: a cache hit is what makes an entry the most recently
	// used (FUSE re-resolves per read, SMB per handle).
	a2 := get(ha)
	a2.Close()

	ec := get(hc) // admits C and must evict the least recently used: B
	defer ec.Close()

	if n := count(hb).Load(); n != 1 {
		t.Fatalf("B downloaded %d times during admission", n)
	}
	eb2 := get(hb) // B was evicted (LRU), so this re-downloads
	defer eb2.Close()
	if n := count(hb).Load(); n != 2 {
		t.Errorf("B was not evicted despite being least recently used (downloads=%d)", n)
	}
	if n := count(ha).Load(); n != 1 {
		t.Errorf("the recently used A was evicted instead (downloads=%d)", n)
	}
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n] + "..."
}
