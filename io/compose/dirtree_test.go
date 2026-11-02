package compose

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// fakeFilehost is an in-memory tie filehost: GET /{hash} serves the blob map
// (404 for unknown hashes) and PUT /upload/{hash} stores the body under its
// computed content address, echoing the address back as the body — the shape
// putlib's UploadMultipart checks. hits counts GETs per hash so tests can pin
// memoisation (review R3).
type fakeFilehost struct {
	mu    sync.Mutex
	blobs map[string]string
	hits  map[string]int
}

func newFakeFilehost(t *testing.T, blobs map[string]string) (*fakeFilehost, string) {
	t.Helper()
	f := &fakeFilehost{blobs: blobs, hits: make(map[string]int)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			hash := strings.TrimPrefix(r.URL.Path, "/")
			f.mu.Lock()
			f.hits[hash]++
			body, ok := f.blobs[hash]
			f.mu.Unlock()
			if !ok {
				http.Error(w, "no such blob", http.StatusNotFound)
				return
			}
			io.WriteString(w, body)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			addr, err := metadata.HashReader(strings.NewReader(string(body)))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			f.mu.Lock()
			f.blobs[addr] = string(body)
			f.mu.Unlock()
			io.WriteString(w, addr)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeFilehost) hitCount(hash string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[hash]
}

func (f *fakeFilehost) totalGets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.hits {
		n += c
	}
	return n
}

// dirManifest renders a tiedir-v2 blob from entries.
func dirManifest(entries ...metadata.DirEntry) string {
	var b strings.Builder
	b.WriteString(metadata.DirHeader)
	for _, e := range entries {
		b.WriteString(e.Line())
	}
	return b.String()
}

// dirEntry is a manifest entry whose head marks it a directory; fileEntry a
// plain file entry.
func dirEntry(name, hash string) metadata.DirEntry {
	return metadata.DirEntry{Hash: hash, Filename: name, Size: 10, Head: []byte(metadata.DirHeader)}
}

func fileEntry(name, hash string) metadata.DirEntry {
	return metadata.DirEntry{Hash: hash, Filename: name, Size: 3, Head: []byte("abc")}
}

// R3: a diamond DAG (two parents sharing one child hash) must fetch the shared
// manifest exactly once, and both occurrences must keep their own paths.
func TestDirTreeDiamondFetchesSharedChildOnce(t *testing.T) {
	shared, leaf := hashOf("shared"), hashOf("leaf")
	rootHash, aHash, bHash := hashOf("root"), hashOf("a"), hashOf("b")
	blobs := map[string]string{
		rootHash: dirManifest(dirEntry("a", aHash), dirEntry("b", bHash)),
		aHash:    dirManifest(dirEntry("c", shared)),
		bHash:    dirManifest(dirEntry("c", shared)),
		shared:   dirManifest(fileEntry("f.tsv", leaf)),
		leaf:     "abc",
	}
	f, url := newFakeFilehost(t, blobs)

	items, err := DirTree(url, rootHash, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.hitCount(shared) != 1 {
		t.Fatalf("shared manifest fetched %d times, want exactly 1", f.hitCount(shared))
	}
	root := items[0]
	if len(root.Children) != 2 {
		t.Fatalf("root children: %d, want 2", len(root.Children))
	}
	a, b := root.Children[0], root.Children[1]
	if len(a.Children) != 1 || len(b.Children) != 1 {
		t.Fatal("diamond parents lost their shared child")
	}
	// The memoised subtree is cloned per occurrence, so paths stay distinct.
	if a.Children[0].Path != "a/c" || b.Children[0].Path != "b/c" {
		t.Fatalf("shared subtree paths aliased: %q vs %q", a.Children[0].Path, b.Children[0].Path)
	}
	if a.Children[0].Children[0].Path != "a/c/f.tsv" || b.Children[0].Children[0].Path != "b/c/f.tsv" {
		t.Fatal("shared subtree leaves lost their per-occurrence paths")
	}
}

// R3: a manifest that names itself as a child must terminate at maxDirDepth,
// not loop forever and not be papered over by the memo cache.
func TestDirTreeSelfReferenceHitsDepthLimit(t *testing.T) {
	self := hashOf("self")
	blobs := map[string]string{self: dirManifest(dirEntry("again", self))}
	_, url := newFakeFilehost(t, blobs)

	_, err := DirTree(url, self, "")
	if err == nil {
		t.Fatal("expected a depth-limit error for a self-referencing manifest")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d levels", maxDirDepth)) {
		t.Fatalf("expected the maxDirDepth error, got: %v", err)
	}
}

// R3: the node budget must fire even on a shallow but wide tree, where depth
// limits are useless. dirTreeWithBudget is the internal seam for the limit.
func TestDirTreeNodeBudget(t *testing.T) {
	wide := hashOf("wide")
	entries := make([]metadata.DirEntry, 0, 10)
	for i := range 10 {
		entries = append(entries, fileEntry(fmt.Sprintf("f%02d", i), hashOf(fmt.Sprintf("f%02d", i))))
	}
	blobs := map[string]string{wide: dirManifest(entries...)}
	_, url := newFakeFilehost(t, blobs)

	_, err := dirTreeWithBudget(url, wide, "", 5)
	if err == nil {
		t.Fatal("expected the node budget to reject a 10-entry tree with a budget of 5")
	}
	if !strings.Contains(err.Error(), "manifest too large") {
		t.Fatalf("expected the node-budget error, got: %v", err)
	}
	// The production budget must be the documented 1e6.
	if maxDirNodes != 1_000_000 {
		t.Fatalf("maxDirNodes = %d, want 1000000", maxDirNodes)
	}
	// The same tree fits comfortably under the real budget.
	if _, err := DirTree(url, wide, ""); err != nil {
		t.Fatalf("10-entry tree under the production budget: %v", err)
	}
}

// B9j: an Optional spec whose tree cannot be fetched is skipped, so Compose
// succeeds with the remaining specs; a non-Optional one still fails.
func TestComposeOptionalSkipsUnfetchableTree(t *testing.T) {
	good := hashOf("good")
	goodLeaf := hashOf("goodleaf")
	missing := hashOf("missing")
	blobs := map[string]string{
		good:     dirManifest(fileEntry("keep.csv", goodLeaf)),
		goodLeaf: "abc",
	}
	f, url := newFakeFilehost(t, blobs)

	hash, err := Build(url, "store", []Spec{
		{Hash: good},
		{Hash: missing, Paths: []string{"never.csv"}, Optional: true},
	})
	if err != nil {
		t.Fatalf("optional unfetchable spec must be skipped, got: %v", err)
	}
	// The composed root must be the uploaded manifest, retrievable from the
	// fake filehost, and must still carry the good spec's file.
	if got := f.blobs[hash]; !strings.Contains(got, "keep.csv") {
		t.Fatalf("composed manifest %q missing the required file", got)
	}

	_, err = Build(url, "store", []Spec{{Hash: missing}})
	if err == nil {
		t.Fatal("non-optional unfetchable spec must fail Compose")
	}
	if !strings.Contains(err.Error(), "bad status") {
		t.Fatalf("error should surface the fetch failure, got: %v", err)
	}
}

// B9j: an empty Hash must be rejected with ResolveSteps' message before any
// HTTP call — otherwise Compose GETs the filehost root.
func TestComposeRejectsEmptyHashWithoutFetch(t *testing.T) {
	f, url := newFakeFilehost(t, map[string]string{})

	_, err := Build(url, "store", []Spec{{Hash: "", Paths: []string{"a.csv"}}})
	if err == nil {
		t.Fatal("expected an empty-hash spec to be rejected")
	}
	if err.Error() != "input: source has neither a step nor a hash" {
		t.Fatalf("message must match ResolveSteps, got: %v", err)
	}
	if f.totalGets() != 0 {
		t.Fatalf("empty hash must not trigger any GET, got %d", f.totalGets())
	}
}
