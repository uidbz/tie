package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/tie/metadata"
)

// captureLogs points the default logger at a buffer for the duration of the
// test (the reaper reports through slog).
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func blobExists(t *testing.T, srv *server, hash string) bool {
	t.Helper()
	st, ok := srv.findBlob(hash)
	if !ok {
		return false
	}
	path, err := PathFromHash(st.path, hash)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(path)
	return err == nil
}

// TestReapDryRunReportsWithoutTouchingAnything is the §9 guard-rail: the reaper
// is destructive, time-based and cross-store, so a pass must be inspectable
// before it is trusted. A dry run marks and reports exactly like a real one and
// leaves every byte and index entry alone.
func TestReapDryRunReportsWithoutTouchingAnything(t *testing.T) {
	logs := captureLogs(t)

	dir := t.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	past := now - 100

	st := srv.storeByName("s")
	orphan := hexHash("a1")
	child := hexHash("a2")
	dirHash := hexHash("a3")
	writeBlob(t, st, orphan, []byte("orphan"))
	writeBlob(t, st, child, []byte("child"))
	entry := metadata.DirEntry{Hash: child, Filename: "kid", Size: 5}
	writeBlob(t, st, dirHash, []byte(metadata.DirHeader+entry.Line()))
	setExpired(t, st, orphan, past)
	setExpired(t, st, child, past)

	wouldDelete, kept := srv.reapOnce(now, true)
	if wouldDelete != 1 || kept != 1 {
		t.Errorf("dry run = (%d, %d), want (1, 1)", wouldDelete, kept)
	}

	// The point of the mode: the log names what would go, so a retention policy
	// can be checked against a real store.
	if !strings.Contains(logs.String(), orphan) {
		t.Errorf("the dry run did not name the blob it would remove:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "dry run") {
		t.Errorf("the log does not say this was a dry run:\n%s", logs.String())
	}

	for _, h := range []string{orphan, child, dirHash} {
		if !blobExists(t, srv, h) {
			t.Errorf("the dry run removed %s", h)
		}
	}
	st.retention.mu.Lock()
	_, hasEntry := st.retention.entries[orphan]
	st.retention.mu.Unlock()
	if !hasEntry {
		t.Error("the dry run pruned the index entry")
	}

	// The same pass for real does remove the orphan and still protects the
	// referenced child.
	deleted, kept := srv.reapOnce(now, false)
	if deleted != 1 || kept != 1 {
		t.Errorf("real pass = (%d, %d), want (1, 1)", deleted, kept)
	}
	if blobExists(t, srv, orphan) {
		t.Error("the real pass did not remove the orphan")
	}
	if !blobExists(t, srv, child) {
		t.Error("the real pass reaped a referenced child")
	}
}

// TestDryRunHonoursTheRecheck: a dry run must describe what a real pass would do
// *now*, so it re-checks each candidate under the index lock like the sweep does.
// A blob re-uploaded (re-extended) after the expired-set snapshot is not a
// candidate any more.
func TestDryRunHonoursTheRecheck(t *testing.T) {
	captureLogs(t)

	dir := t.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	st := srv.storeByName("s")

	gone := hexHash("b1")
	back := hexHash("b2")
	writeBlob(t, st, gone, []byte("expired"))
	writeBlob(t, st, back, []byte("re-uploaded"))
	setExpired(t, st, gone, now-10)
	setExpired(t, st, back, now-10)

	// Both are in the snapshot; `back` is re-extended before the sweep runs.
	st.retention.mu.Lock()
	e := st.retention.entries[back]
	e.ExpiresAt = now + 3600
	st.retention.entries[back] = e
	st.retention.mu.Unlock()

	if wouldDelete, _ := srv.reapOnce(now, true); wouldDelete != 1 {
		t.Errorf("dry run would remove %d blobs, want 1 (the re-extended one is no longer a candidate)", wouldDelete)
	}
	if !blobExists(t, srv, back) || !blobExists(t, srv, gone) {
		t.Error("a dry run touched the store")
	}
}

// TestDryRunLeavesShardDirsAlone: a real sweep prunes the shard directories a
// deleted blob leaves empty; a dry run must not rearrange the tree.
func TestDryRunLeavesShardDirsAlone(t *testing.T) {
	captureLogs(t)

	dir := t.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	st := srv.storeByName("s")

	orphan := hexHash("c1")
	writeBlob(t, st, orphan, []byte("orphan"))
	setExpired(t, st, orphan, now-10)

	path, err := PathFromHash(st.path, orphan)
	if err != nil {
		t.Fatal(err)
	}
	shard := filepath.Dir(path)

	if n, _ := srv.reapOnce(now, true); n != 1 {
		t.Fatalf("dry run would remove %d blobs, want 1", n)
	}
	if _, err := os.Stat(shard); err != nil {
		t.Errorf("the dry run pruned the shard directory %s: %v", shard, err)
	}

	if n, _ := srv.reapOnce(now, false); n != 1 {
		t.Fatalf("real pass removed %d blobs, want 1", n)
	}
	if _, err := os.Stat(shard); !os.IsNotExist(err) {
		t.Errorf("the real pass left the empty shard directory %s", shard)
	}
}
