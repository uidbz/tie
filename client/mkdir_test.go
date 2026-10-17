package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// TestMkTieDirAllConcurrent is the §4.10 guard: two creators racing on the
// same path both asked "is there a UID for this path?", both got "no", and both
// minted one — the duplicate-path state that verify reports and the dedup
// script exists to clean up.
func TestMkTieDirAllConcurrent(t *testing.T) {
	tie := freshVerifyClient(t)

	const racers = 6
	var wg sync.WaitGroup
	leaf := make([]DirUID, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uid, err := tie.MkTieDirAll("tie:/race/album/disc1")
			if err != nil {
				t.Errorf("racer %d: %v", i, err)
				return
			}
			leaf[i] = uid
		}(i)
	}
	wg.Wait()

	for i, uid := range leaf {
		if uid != leaf[0] {
			t.Fatalf("racer %d made the leaf %s, racer 0 made %s", i, uid, leaf[0])
		}
	}

	for _, p := range []string{"tie:/race", "tie:/race/album", "tie:/race/album/disc1"} {
		rows, _, err := tie.Query(QuerySpec{Terms: []string{p}, Reverse: true, Filter: string(RelPath)})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Errorf("%d UIDs claim %q — a duplicate path", len(rows), p)
		}
	}
}

// TestMkTieDirSentinel: the "already there" case is a sentinel, not a string
// an err.Error() suffix match happens to match.
func TestMkTieDirSentinel(t *testing.T) {
	tie := freshVerifyClient(t)

	uid, err := tie.MkTieDirAll("tie:/sentinel/inner")
	if err != nil {
		t.Fatal(err)
	}

	again, err := tie.MkTieDir("tie:/sentinel/inner")
	if !errors.Is(err, ErrDirExists) {
		t.Errorf("MkTieDir on an existing dir = %v, want an error wrapping ErrDirExists", err)
	}
	if again != uid {
		t.Errorf("the existing UID must come back: got %q, want %q", again, uid)
	}

	if err := tie.CreateTieRootDir(); !errors.Is(err, ErrDirExists) {
		t.Errorf("CreateTieRootDir on an existing root = %v, want ErrDirExists", err)
	}
}

// TestMkTieDirNeedsItsParent: the old code wrote a parent edge to the empty
// string when the parent did not exist, quietly creating an orphan.
func TestMkTieDirNeedsItsParent(t *testing.T) {
	tie := freshVerifyClient(t)

	if _, err := tie.MkTieDir("tie:/no-such-parent/child"); err == nil {
		t.Error("creating under a missing parent succeeded")
	}
	rows, _, err := tie.Query(QuerySpec{Terms: []string{"tie:/no-such-parent/child"}, Reverse: true, Filter: string(RelPath)})
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a node for %q was written anyway", "tie:/no-such-parent/child")
	}
}

// TestVirtualDir pins the parent computation: virtual tie paths are slash
// separated, so this must be path.Dir, never filepath.Dir (which on Windows
// splits on a backslash and hands the store a parent it has never seen).
func TestVirtualDir(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"tie:/a/b/c", "/a/b"},
		{"tie:/a/b", "/a"},
		{"tie:/a", "/"},
		{"tie:/", "/"},
		{"/a/b", "/a"},
		{"tie:/a//b/", "/a/b"},
	}
	for _, c := range cases {
		if got := virtualDir(c.in); got != c.want {
			t.Errorf("virtualDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// countingProxy counts the requests that reach the triplestore.
func countingProxy(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		req, err := http.NewRequest(r.Method, testingTripleStoreURL+r.URL.Path, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if u, p, ok := r.BasicAuth(); ok {
			req.SetBasicAuth(u, p)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// TestMkTieDirAllCostsOneRequestPerLevel: the old walk asked for the UID of
// every ancestor, and for its parent, on every call — up to three round trips
// per level even when the whole chain already existed.
func TestMkTieDirAllCostsOneRequestPerLevel(t *testing.T) {
	proxy, count := countingProxy(t)

	cfg := verifyTestConfig()
	cfg.TripleStoreURL = proxy.URL
	tie := NewTieClient(cfg)

	const deep = "tie:/deep/a/b/c/d/e"
	if _, err := tie.MkTieDirAll(deep); err != nil {
		t.Fatal(err)
	}
	count.Store(0)
	if _, err := tie.MkTieDirAll(deep); err != nil {
		t.Fatal(err)
	}

	// One MkDir for the root plus one per level; nothing else.
	const levels = 6
	if got := count.Load(); got > levels+1 {
		t.Errorf("the second MkTieDirAll cost %d requests, want at most %d (one per level)", got, levels+1)
	}
}
