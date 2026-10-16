package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
)

// testHost is a running single-store filehost for a test: the filehost server
// itself (for store-level assertions like findBlob) plus its HTTP front end.
type testHost struct {
	s  *server
	hs *httptest.Server
	st *blobStore
}

// newTestServer builds a single-store filehost on a temp dir.
func newTestServer(t *testing.T) *testHost {
	t.Helper()
	dir := t.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "default", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.routes(mux, auth.NewStore(nil, auth.RoleWrite))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return &testHost{s: srv, hs: hs, st: srv.storeByName("default")}
}

// TestHashRoutesRejectTraversal is the regression test for the path-traversal
// hole: ServeMux unescapes %2F inside a wildcard, so a 64-char value of
// "..%2F" repetitions used to reach PathFromHash and serve arbitrary files.
// Every {hash} route must now answer 400.
func TestHashRoutesRejectTraversal(t *testing.T) {
	h := newTestServer(t)
	trav := strings.Repeat("..%2F", 18) + "etc%2Fpasswd" // 64 chars when unescaped
	if n := len(strings.ReplaceAll(trav, "%2F", "/")); n != 64 {
		t.Fatalf("probe length %d, want 64", n)
	}
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/" + trav},
		{http.MethodHead, "/" + trav},
		{http.MethodGet, "/" + trav + "/name"},
		{http.MethodPut, "/upload/" + trav},
		{http.MethodPut, "/retention/" + trav},
		{http.MethodGet, "/retention/" + trav},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, h.hs.URL+c.path, strings.NewReader("x"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s traversal probe: status = %d, want 400", c.method, resp.StatusCode)
		}
	}
}

// TestPathFromHashRejectsNonHex guards the defence-in-depth layer.
func TestPathFromHashRejectsNonHex(t *testing.T) {
	for _, bad := range []string{"", "..", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("../", 21) + "etc/passwd"} {
		if p, err := PathFromHash("/root", bad); err == nil {
			t.Errorf("PathFromHash(%q) = %q, want error", bad, p)
		}
	}
	if p, err := PathFromHash("/root", hexHash("ab")); err != nil || p != filepath.Join("/root", "ab", strings.Repeat("0", 62)[0:2], hexHash("ab")) {
		t.Errorf("PathFromHash valid hash = %q, %v", p, err)
	}
}

// TestUploadChecksumMismatchIs422 verifies a mismatched upload fails with 422,
// stores nothing, and records no retention entry.
func TestUploadChecksumMismatchIs422(t *testing.T) {
	h := newTestServer(t)
	st := h.st
	req, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload/"+hexHash("ff"), strings.NewReader("actual content"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422; body %q", resp.StatusCode, body)
	}
	if _, ok := h.s.findBlob(hexHash("ff")); ok {
		t.Error("mismatched upload stored a blob at the claimed address")
	}
	if _, ok := st.retention.get(hexHash("ff")); ok {
		t.Error("mismatched upload recorded retention")
	}
	// No temp-file litter left in the store.
	if leftovers := listBlobs(st.path); len(leftovers) != 0 {
		t.Errorf("store not clean after failed upload: %v", leftovers)
	}
}

// errMidStream fails partway through the body, simulating a dropped
// connection or a crash mid-upload.
type errMidStream struct {
	read int
	data []byte
}

func (e *errMidStream) Read(p []byte) (int, error) {
	if e.read >= len(e.data)/2 {
		return 0, errors.New("connection reset")
	}
	n := copy(p, e.data[e.read:])
	e.read += n
	return n, nil
}

// TestUploadInterruptedLeavesNoBlob is the §2.3 regression: an interrupted
// upload must never leave a truncated file at the content address.
func TestUploadInterruptedLeavesNoBlob(t *testing.T) {
	h := newTestServer(t)
	st := h.st
	// Large enough that the transport cannot have buffered the whole body:
	// with a few KB the server receives every byte before the client's error
	// surfaces, and the store legitimately holds the blob (which made this
	// test flaky).
	content := bytes.Repeat([]byte("payload-"), 512*1024) // 4 MiB
	want, err := metadata.HashReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload/"+want, &errMidStream{data: content})
	req.ContentLength = int64(len(content))
	resp, err := http.DefaultClient.Do(req)
	if err == nil { // server-side copy error surfaces as 500 or a transport error; either is fine
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("interrupted upload reported success")
		}
	}
	// Wait for the handler to finish before inspecting the store: otherwise the
	// assertions race the server goroutine.
	h.hs.Close()
	if _, ok := h.s.findBlob(want); ok {
		t.Error("interrupted upload left a blob at the content address")
	}
	if leftovers := listBlobs(st.path); len(leftovers) != 0 {
		t.Errorf("temp upload litter left in store: %v", leftovers)
	}
}

// TestConcurrentSameHashUpload verifies two racing uploads of identical
// content both succeed and the stored bytes are intact (the old O_APPEND
// path interleaved them).
func TestConcurrentSameHashUpload(t *testing.T) {
	h := newTestServer(t)
	content := strings.Repeat("race-condition-probe", 5000)
	want, err := metadata.HashReader(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload/"+want, strings.NewReader(content))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				codes[i] = -1
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			codes[i] = resp.StatusCode
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("upload %d: status %d, want 200", i, c)
		}
	}
	got, err := metadata.HashReader(strings.NewReader(readBlob(t, h, want)))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("stored content hash %s, want %s (interleaved?)", got, want)
	}
}

// TestUploadExistingHashDedup verifies re-uploading existing bytes returns
// the hash without corrupting the stored copy.
func TestUploadExistingHashDedup(t *testing.T) {
	h := newTestServer(t)
	content := "dedup me"
	want, _ := metadata.HashReader(strings.NewReader(content))
	req, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload/"+want, strings.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first upload status %d", resp.StatusCode)
	}
	req2, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload/"+want, strings.NewReader(content))
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || strings.TrimSpace(string(b)) != want {
		t.Errorf("dedup upload: status %d body %q, want 200 %q", resp2.StatusCode, b, want)
	}
	if readBlob(t, h, want) != content {
		t.Error("dedup upload corrupted the stored blob")
	}
}

// TestHashlessUploadStaysInStore verifies the hashless path stages inside the
// store (no /tmp EXDEV class of bug) and lands the blob.
func TestHashlessUploadStaysInStore(t *testing.T) {
	h := newTestServer(t)
	st := h.st
	req, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload", strings.NewReader("hashless content"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	hash := strings.TrimSpace(string(b))
	if resp.StatusCode != http.StatusOK || !metadata.IsHexHash(hash) {
		t.Fatalf("hashless upload: status %d body %q", resp.StatusCode, b)
	}
	if _, ok := h.s.findBlob(hash); !ok {
		t.Fatal("hashless upload did not land in the store")
	}
	if leftovers := listBlobs(st.path); len(leftovers) != 1 {
		t.Errorf("expected exactly the blob in the store, got %v", leftovers)
	}
}

func readBlob(t *testing.T, h *testHost, hash string) string {
	t.Helper()
	st, ok := h.s.findBlob(hash)
	if !ok {
		t.Fatalf("blob %s not found", hash)
	}
	p, err := PathFromHash(st.path, hash)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestReaperMidPassDirectoryProtection verifies a directory blob written
// during the pass still protects its expired children (it was not in the
// mark-phase walk).
func TestReaperMidPassDirectoryProtection(t *testing.T) {
	dir := t.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	st := srv.storeByName("s")
	child := hexHash("77")
	dirHash := hexHash("88")
	writeBlob(t, st, child, []byte("child"))
	setExpired(t, st, child, now-10)

	// The directory blob lands after the pass started (fresh mtime).
	entry := metadata.DirEntry{Hash: child, Filename: "kid", Size: 5}
	writeBlob(t, st, dirHash, []byte(metadata.DirHeader+entry.Line()))
	// Not indexed: it landed after the pass started, which is what the
	// mid-pass re-check exists to catch.

	deleted, kept := srv.reapOnce(now)
	if deleted != 0 || kept != 1 {
		t.Errorf("reapOnce = (%d, %d), want (0, 1)", deleted, kept)
	}
	if _, ok := srv.findBlob(child); !ok {
		t.Error("child of a mid-pass directory blob was reaped")
	}
}

// TestReaperRechecksBeforeDelete covers the TOCTOU fix: an entry re-extended
// (or made permanent) after the expired-set snapshot must survive the sweep.
func TestReaperRechecksBeforeDelete(t *testing.T) {
	dir := t.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	st := srv.storeByName("s")
	gone := hexHash("98")
	kept := hexHash("99")
	writeBlob(t, st, gone, []byte("expired for real"))
	writeBlob(t, st, kept, []byte("valuable"))
	setExpired(t, st, gone, now-10)
	setExpired(t, st, kept, now-10)

	// Simulate the race: both hashes are in the sweep list (the snapshot),
	// but `kept` was made permanent before the sweep acquires the lock.
	st.retention.mu.Lock()
	e := st.retention.entries[kept]
	e.ExpiresAt = 0
	st.retention.entries[kept] = e
	st.retention.mu.Unlock()
	sweepStore(st, []string{gone, kept}, now)

	if _, ok := srv.findBlob(gone); ok {
		t.Error("genuinely expired blob was not reaped")
	}
	if _, ok := srv.findBlob(kept); !ok {
		t.Error("re-extended blob was reaped despite the re-check")
	}
	if _, ok := st.retention.get(kept); !ok {
		t.Error("re-extended blob lost its index entry")
	}
}

// TestDirIndexTracksUploads is the §5.2(9) regression: the reaper used to open
// every blob in the store on every pass looking for manifests. Uploads now
// record the manifests they are, the index survives a restart, and a store
// predating the file is bootstrapped once.
func TestDirIndexTracksUploads(t *testing.T) {
	h := newTestServer(t)
	st := h.st

	child := hexHash("a1")
	body := bytes.NewReader([]byte("kid"))
	req, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	childBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	childHash := strings.TrimSpace(string(childBytes))

	entry := metadata.DirEntry{Hash: childHash, Filename: "kid", Size: 3}
	manifest := metadata.DirHeader + entry.Line()
	req, _ = http.NewRequest(http.MethodPut, h.hs.URL+"/upload", strings.NewReader(manifest))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	dirBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	dirHash := strings.TrimSpace(string(dirBytes))
	_ = child

	if got := st.dirs.all(); len(got) != 1 || got[0] != dirHash {
		t.Fatalf("dir index = %v, want exactly the manifest %s", got, dirHash)
	}

	// A plain blob upload must not be indexed as a manifest.
	req, _ = http.NewRequest(http.MethodPut, h.hs.URL+"/upload", strings.NewReader("not a dir"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(st.dirs.all()) != 1 {
		t.Errorf("a plain blob was indexed as a directory: %v", st.dirs.all())
	}

	// Reopening the store reads the index back instead of rescanning.
	srv2 := &server{}
	if err := srv2.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "default", Path: st.path}}}); err != nil {
		t.Fatal(err)
	}
	if got := srv2.storeByName("default").dirs.all(); len(got) != 1 || got[0] != dirHash {
		t.Errorf("dir index after restart = %v, want %s", got, dirHash)
	}
}
