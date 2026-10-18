package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uidbz/tie/metadata"
)

// TestInitStoresDoesNotScanTheStore: the dir index used to be built inside
// initStores, which opens every blob in the store. On a large cold store that
// is minutes during which the listener is not yet open — the service looks
// alive in the log (it says "starting") and refuses every connection, and a
// supervisor that respawns restarts it before the scan can finish, forever.
// Start-up must only read the index.
func TestInitStoresDoesNotScanTheStore(t *testing.T) {
	dir := t.TempDir()
	st := &blobStore{path: dir}
	child := hexHash("b1")
	dirHash := hexHash("b2")
	writeBlob(t, st, child, []byte("child"))
	writeBlob(t, st, dirHash, []byte(metadata.DirHeader+metadata.DirEntry{Hash: child, Filename: "kid", Size: 5}.Line()))

	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: dir}}}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, dirIndexFileName)); !os.IsNotExist(err) {
		t.Error("start-up scanned the store: the dir index exists before a request was ever served")
	}
	if srv.storeByName("s").dirs.ready {
		t.Error("a store that was never scanned reports a ready index")
	}
}

// TestReaperBuildsDirIndexBeforeSweeping: moving the scan off start-up is only
// safe if the mark phase cannot run on an index that has not seen the store.
// Here nothing has ever indexed the store, and the only thing standing between a
// live directory's child and the sweep is that scan.
func TestReaperBuildsDirIndexBeforeSweeping(t *testing.T) {
	dir := t.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	st := srv.storeByName("s")

	child := hexHash("c1")
	dirHash := hexHash("c2")
	writeBlob(t, st, child, []byte("child"))
	writeBlob(t, st, dirHash, []byte(metadata.DirHeader+metadata.DirEntry{Hash: child, Filename: "kid", Size: 5}.Line()))
	now := time.Now().Unix()
	setExpired(t, st, child, now-100)

	if deleted, kept := srv.reapOnce(now, false); deleted != 0 || kept != 1 {
		t.Errorf("reapOnce = (%d, %d), want (0, 1): the directory's child must be protected", deleted, kept)
	}
	if !blobExists(t, srv, child) {
		t.Error("the sweep deleted a blob referenced by a directory the index had never seen")
	}

	di, _, err := loadDirIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !di.ready || !di.dirs[dirHash] {
		t.Errorf("the reaper did not record a completed index: ready=%v dirs=%v", di.ready, di.dirs)
	}
}

// TestReaperRescansInterruptedDirIndex is the data-loss case the completion
// marker exists for. An index file can appear without ever having seen the
// store — the first upload of a directory blob creates it with one line. The
// cheap repair path only looks at blobs newer than that file, so every
// directory that predates it is invisible, and the blobs under them are
// unprotected. An index without the marker must be re-scanned, not trusted.
func TestReaperRescansInterruptedDirIndex(t *testing.T) {
	dir := t.TempDir()
	st := &blobStore{path: dir}
	child := hexHash("d1")
	dirHash := hexHash("d2")
	writeBlob(t, st, child, []byte("child"))
	writeBlob(t, st, dirHash, []byte(metadata.DirHeader+metadata.DirEntry{Hash: child, Filename: "kid", Size: 5}.Line()))

	// The blobs predate the index by an hour, as they do in a real store that
	// ran for months before the first directory upload created the file.
	old := time.Now().Add(-time.Hour)
	for _, h := range []string{child, dirHash} {
		p, err := st.makeDestinationPath(h)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	// A partial index: one unrelated hash, written after every blob above, so
	// the cheap repair path would consider the store already covered.
	if err := os.WriteFile(filepath.Join(dir, dirIndexFileName), []byte(hexHash("d9")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: dir}}}); err != nil {
		t.Fatal(err)
	}
	if srv.storeByName("s").dirs.ready {
		t.Error("an index without the completion marker is treated as complete")
	}

	now := time.Now().Unix()
	setExpired(t, srv.storeByName("s"), child, now-100)

	if deleted, kept := srv.reapOnce(now, false); deleted != 0 || kept != 1 {
		t.Errorf("reapOnce = (%d, %d), want (0, 1): a partial index must not be trusted to mark", deleted, kept)
	}
	if !blobExists(t, srv, child) {
		t.Error("a directory the partial index never mentioned lost its child to the sweep")
	}
}
