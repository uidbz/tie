package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
)

func hexHash(prefix string) string {
	return prefix + strings.Repeat("0", 64-len(prefix))
}

func writeBlob(t *testing.T, st *blobStore, hash string, data []byte) {
	t.Helper()
	p, err := st.makeDestinationPath(hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func setExpired(t *testing.T, st *blobStore, hash string, at int64) {
	t.Helper()
	st.retention.mu.Lock()
	st.retention.entries[hash] = blobRetention{ExpiresAt: at, UpdatedAt: at}
	st.retention.mu.Unlock()
}

func TestInitStoresLegacyBlobPath(t *testing.T) {
	dir := t.TempDir()
	if err := initStores(FilehostConfig{BlobPath: dir}); err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 {
		t.Fatalf("want 1 synthesized store, got %d", len(stores))
	}
	if defaultStore == nil || defaultStore.name != "default" {
		t.Fatalf("default store not synthesized from BlobPath")
	}
	if storeByName("") != defaultStore {
		t.Fatal("empty Tie-Store should map to the default store")
	}
	if defaultStore.defaultRetention != 0 {
		t.Fatal("legacy store should be permanent (0 retention)")
	}
}

func TestInitStoresMultiAndDefault(t *testing.T) {
	ssd, hdd := t.TempDir(), t.TempDir()
	err := initStores(FilehostConfig{BlobPaths: []BlobPathConfig{
		{Name: "ssd", Path: ssd},
		{Name: "hdd", Path: hdd, DefaultRetention: "1h"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// No store named "default": the first listed is the default.
	if storeByName("") != stores[0] || stores[0].name != "ssd" {
		t.Fatal("default should be the first listed store when none is named default")
	}
	if h := storeByName("hdd"); h == nil || h.defaultRetention != time.Hour {
		t.Fatalf("hdd store retention not parsed: %+v", h)
	}
	if storeByName("nope") != nil {
		t.Fatal("unknown store name should resolve to nil")
	}
}

func TestInitStoresErrors(t *testing.T) {
	d := t.TempDir()
	if err := initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "a", Path: d}, {Name: "a", Path: d}}}); err == nil {
		t.Fatal("expected duplicate store name error")
	}
	if err := initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "a", Path: d, DefaultRetention: "not-a-duration"}}}); err == nil {
		t.Fatal("expected invalid DefaultRetention error")
	}
	if err := initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "", Path: d}}}); err == nil {
		t.Fatal("expected empty-name error")
	}
}

func TestFindBlobSearchesAllStores(t *testing.T) {
	ssd, hdd := t.TempDir(), t.TempDir()
	if err := initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "ssd", Path: ssd}, {Name: "hdd", Path: hdd}}}); err != nil {
		t.Fatal(err)
	}
	h := hexHash("ab")
	writeBlob(t, storeByName("hdd"), h, []byte("payload"))
	st, ok := findBlob(h)
	if !ok || st.name != "hdd" {
		t.Fatalf("findBlob = (%v, %v), want hdd", st, ok)
	}
	if _, ok := findBlob(hexHash("cd")); ok {
		t.Fatal("findBlob reported an absent blob as present")
	}
}

func TestUploadExpiry(t *testing.T) {
	d := t.TempDir()
	if err := initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: d, DefaultRetention: "2h"}}}); err != nil {
		t.Fatal(err)
	}
	st := storeByName("s")
	now := time.Now()

	r := httptest.NewRequest("PUT", "/upload", nil)
	got, err := st.uploadExpiry(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(2 * time.Hour).Unix(); got != want {
		t.Errorf("no header: expiry = %d, want store default %d", got, want)
	}

	r.Header.Set("Tie-Retention", "30m")
	if got, _ := st.uploadExpiry(r, now); got != now.Add(30*time.Minute).Unix() {
		t.Errorf("header should override store default, got %d", got)
	}

	r.Header.Set("Tie-Retention", "infinite")
	if got, _ := st.uploadExpiry(r, now); got != 0 {
		t.Errorf("infinite header should yield 0 (permanent), got %d", got)
	}
}

// TestReapCrossStoreProtection verifies a directory blob in one store protects
// an expired child that lives in a different store, while an unreferenced
// expired blob in that store is still swept.
func TestReapCrossStoreProtection(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "a", Path: a}, {Name: "b", Path: b}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	past := now - 100

	child := hexHash("11")   // in b: expired but referenced by a's dir
	orphan := hexHash("22")  // in b: expired and unreferenced
	dirHash := hexHash("33") // in a: permanent, references child

	writeBlob(t, storeByName("b"), child, []byte("child"))
	writeBlob(t, storeByName("b"), orphan, []byte("orphan"))
	entry := metadata.DirEntry{Hash: child, Filename: "kid", Size: 5}
	writeBlob(t, storeByName("a"), dirHash, []byte(metadata.DirHeader+entry.Line()))

	setExpired(t, storeByName("b"), child, past)
	setExpired(t, storeByName("b"), orphan, past)

	deleted, kept := reapOnce(now)
	if kept != 1 {
		t.Errorf("kept = %d, want 1 (cross-store-referenced child)", kept)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (orphan)", deleted)
	}
	if _, ok := findBlob(child); !ok {
		t.Error("referenced child was reaped despite cross-store protection")
	}
	if _, ok := findBlob(orphan); ok {
		t.Error("unreferenced expired orphan was not reaped")
	}
	if _, ok := findBlob(dirHash); !ok {
		t.Error("permanent directory blob was reaped")
	}
}

// TestUploadRoutingAndSearchDownload drives the HTTP handlers end-to-end: an
// upload with a Tie-Store header lands in that store only, a hash-only download
// finds it by searching stores, and an unknown store is rejected.
func TestUploadRoutingAndSearchDownload(t *testing.T) {
	ssd, hdd := t.TempDir(), t.TempDir()
	if err := initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "ssd", Path: ssd}, {Name: "hdd", Path: hdd}}}); err != nil {
		t.Fatal(err)
	}
	InitKey()
	mux := http.NewServeMux()
	routes(mux, auth.NewStore(nil, auth.RoleWrite))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := "hello multi-store"
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/upload", strings.NewReader(body))
	req.Header.Set("Tie-Store", "hdd")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	hash := strings.TrimSpace(string(hb))
	if !metadata.IsHexHash(hash) {
		t.Fatalf("upload returned non-hash %q", hash)
	}

	if _, err := os.Stat(PathFromHash(hdd, hash)); err != nil {
		t.Errorf("blob not stored in the requested hdd store: %v", err)
	}
	if _, err := os.Stat(PathFromHash(ssd, hash)); err == nil {
		t.Error("blob leaked into the ssd store")
	}

	dresp, err := http.Get(srv.URL + "/" + hash)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(dresp.Body)
	dresp.Body.Close()
	if dresp.StatusCode != http.StatusOK || string(got) != body {
		t.Errorf("hash-only download: status=%d body=%q, want 200 %q", dresp.StatusCode, got, body)
	}

	req2, _ := http.NewRequest(http.MethodPut, srv.URL+"/upload", strings.NewReader("x"))
	req2.Header.Set("Tie-Store", "nope")
	r2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown store upload: status=%d, want 400", r2.StatusCode)
	}
}
