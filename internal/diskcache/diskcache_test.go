package diskcache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestCache(t *testing.T, maxBytes int64) *Cache {
	t.Helper()
	c, err := New(Options{Dir: t.TempDir(), MaxBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func produce(s string) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := w.Write([]byte(s))
		return err
	}
}

func readAll(t *testing.T, h *Handle) string {
	t.Helper()
	out := make([]byte, h.Size())
	if _, err := h.ReadAt(out, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	return string(out)
}

func TestGetSingleFlight(t *testing.T) {
	c := newTestCache(t, 1<<20)
	var runs atomic.Int64
	hold := make(chan struct{})
	started := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := c.Get("k", func(w io.Writer) error {
				if runs.Add(1) == 1 {
					close(started)
					<-hold // keep the producer busy so the others must coalesce
				}
				_, err := w.Write([]byte("payload"))
				return err
			})
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			defer h.Close()
			if got := readAll(t, h); got != "payload" {
				t.Errorf("content = %q", got)
			}
		}()
	}
	<-started
	close(hold)
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Errorf("produce ran %d times, want 1", n)
	}
}

func TestGetFailureIsNotCached(t *testing.T) {
	c := newTestCache(t, 1<<20)
	boom := errors.New("producer failed")
	if _, err := c.Get("k", func(w io.Writer) error { return boom }); err == nil {
		t.Fatal("expected the produce error to surface")
	}
	h, err := c.Get("k", produce("ok now"))
	if err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	defer h.Close()
	if got := readAll(t, h); got != "ok now" {
		t.Errorf("content = %q", got)
	}
}

// TestEvictionKeepsLiveHandleReadable: eviction unlinks the name and forgets
// the entry, but a handle that is mid-read keeps its fd.
func TestEvictionKeepsLiveHandleReadable(t *testing.T) {
	c := newTestCache(t, 100) // fits one ~60-byte object

	a, err := c.Get("a", produce(strings.Repeat("A", 60)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := c.Get("b", produce(strings.Repeat("B", 60))); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, a); got != strings.Repeat("A", 60) {
		t.Errorf("read through the evicted handle returned %q", firstN(got, 12))
	}
	if _, ok := c.Peek("a"); ok {
		t.Error("the evicted entry is still visible to Peek")
	}
}

// TestEvictionIsLRU: a hit must move the entry to the back so the least
// recently used object is the one dropped.
func TestEvictionIsLRU(t *testing.T) {
	c := newTestCache(t, 130) // fits two 60-byte objects

	a, err := c.Get("a", produce(strings.Repeat("A", 60)))
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := c.Get("b", produce(strings.Repeat("B", 60)))
	if err != nil {
		t.Fatal(err)
	}
	b.Close()

	// Touch a: it becomes the most recently used.
	if _, ok := c.Peek("a"); !ok {
		t.Fatal("a missing before the eviction")
	}
	if _, err := c.Get("c", produce(strings.Repeat("C", 60))); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Peek("a"); !ok {
		t.Error("the recently used entry was evicted")
	}
	if _, ok := c.Peek("b"); ok {
		t.Error("the least recently used entry (b) survived the eviction")
	}
}

func TestAdoptSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	c, err := New(Options{Dir: dir, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Get("a", produce(strings.Repeat("A", 60)))
	if err != nil {
		t.Fatal(err)
	}
	path := h.Path()
	h.Close()
	c.Close()

	// A fresh cache over the same directory, without Adopt, knows nothing.
	cold, err := New(Options{Dir: dir, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cold.Peek("a"); ok {
		t.Error("a cold cache reported a cached entry")
	}
	cold.Close()

	warm, err := New(Options{Dir: dir, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the file should still be on disk: %v", err)
	}
	warm.Adopt("a", path, info.Size())
	h2, ok := warm.Peek("a")
	if !ok {
		t.Fatal("Adopt did not make the entry visible")
	}
	defer h2.Close()
	if got := readAll(t, h2); got != strings.Repeat("A", 60) {
		t.Errorf("adopted content = %q", firstN(got, 12))
	}
	if bytes, _ := warm.Stats(); bytes != info.Size() {
		t.Errorf("adopted size = %d, want %d (the budget must count what a previous run left)", bytes, info.Size())
	}
}

func TestFillIsAsync(t *testing.T) {
	c := newTestCache(t, 1<<20)
	hold := make(chan struct{})
	started := make(chan struct{})
	c.Fill("k", func(w io.Writer) error {
		close(started)
		<-hold
		_, err := w.Write([]byte("filled"))
		return err
	})
	<-started
	if _, ok := c.Peek("k"); ok {
		t.Error("Peek reported an entry that is still filling")
	}
	close(hold)
	// The fill completes on its own; poll briefly.
	for i := 0; i < 100; i++ {
		if h, ok := c.Peek("k"); ok {
			defer h.Close()
			if got := readAll(t, h); got != "filled" {
				t.Errorf("filled content = %q", got)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("Fill never published the entry")
}

func TestOversizedObjectIsStillServed(t *testing.T) {
	c := newTestCache(t, 10)
	h, err := c.Get("big", produce(strings.Repeat("X", 500)))
	if err != nil {
		t.Fatalf("an object larger than the budget must still be served: %v", err)
	}
	defer h.Close()
	if h.Size() != 500 {
		t.Errorf("size = %d, want 500", h.Size())
	}
}

func TestLayoutRejectsBadKeys(t *testing.T) {
	c, err := New(Options{
		Dir:      t.TempDir(),
		MaxBytes: 1 << 20,
		Layout: func(dir, key string) (string, error) {
			if strings.Contains(key, "..") {
				return "", errors.New("bad key")
			}
			return filepath.Join(dir, key), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Get("../escape", produce("x")); err == nil {
		t.Error("the layout must be able to reject a key")
	}
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n] + "..."
}

// BenchmarkGetHot measures the per-lookup cost on a cached entry: FUSE
// re-resolves per read, so this is the read path's overhead.
func BenchmarkGetHot(b *testing.B) {
	c, err := New(Options{Dir: b.TempDir(), MaxBytes: 1 << 20})
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	h, err := c.Get("k", produce(strings.Repeat("x", 4096)))
	if err != nil {
		b.Fatal(err)
	}
	h.Close()
	buf := make([]byte, 512)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hh, err := c.Get("k", produce("unused"))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := hh.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
			b.Fatal(err)
		}
		hh.Close()
	}
}

// TestAdoptDoesNotHoldDescriptors is the start-up guard. Indexing a cache
// directory must not cost one descriptor per entry: a filehost with a read
// cache of any size then exhausts the process fd budget before it can open a
// listening socket, and the service dies in a respawn loop without ever
// serving a request. Adopting is a stat, not an open.
func TestAdoptDoesNotHoldDescriptors(t *testing.T) {
	dir := t.TempDir()
	const n = 400
	var paths []string
	for i := range n {
		key := fmt.Sprintf("%064x", i)
		p := dir + "/" + key[:2] + "/" + key[2:4] + "/" + key
		if err := os.MkdirAll(dir+"/"+key[:2]+"/"+key[2:4], 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, key+"|"+p)
	}

	before := openFDs(t)
	c, err := New(Options{Dir: dir, MaxBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, e := range paths {
		key, p, _ := strings.Cut(e, "|")
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		c.Adopt(key, p, info.Size())
	}
	if after := openFDs(t); after-before > 16 {
		t.Errorf("adopting %d entries left %d descriptors open; a cache must hold none", n, after-before)
	}

	// The entries are still usable: the handle opens what adopt did not.
	h, ok := c.Peek(paths[0][:strings.Index(paths[0], "|")])
	if !ok {
		t.Fatal("an adopted entry is not readable")
	}
	defer h.Close()
	if got := readAll(t, h); got != "x" {
		t.Errorf("adopted content = %q", got)
	}
}

// openFDs counts the descriptors this process holds, for the tests that pin
// resource use rather than behaviour.
func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	return len(entries)
}
