package tiedb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReadOnlyCoexistsWithWriter is the engine-level guarantee behind
// `tie --read-only`: a read-only tree takes no lock and no writer, so it loads
// while a write-capable tree holds the same collection. Writes are refused.
func TestReadOnlyCoexistsWithWriter(t *testing.T) {
	dir := t.TempDir()
	key := CollectionKey{Database: dir, Collection: "shared"}

	rw := NewDB(true)
	rw.SetLocking(true)
	col := rw.GetCollection(key)
	if err := col.Add("k", "tag", "v"); err != nil {
		t.Fatal(err)
	}
	col.Sync() // the reader sees on-disk state, not the write queue

	ro := NewDB(true)
	ro.SetReadOnly(true)
	rc, err := ro.TryGetCollection(key)
	if err != nil {
		t.Fatalf("read-only open beside the writer: %v", err)
	}
	if got, ok := rc.Get("k", "tag"); !ok || len(got) != 1 || got[0] != "v" {
		t.Fatalf("read-only Get = %v, %v", got, ok)
	}
	if err := rc.Add("k2", "tag", "v2"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Add = %v, want ErrReadOnly", err)
	}
	if _, ok := rc.Delete("k", "tag", "v"); ok {
		t.Error("Delete succeeded on a read-only collection")
	}
	if _, _, err := rc.MkDir(DirSpec{Path: "tie:/x", NewUID: "u1", PathRelation: "path"}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("MkDir = %v, want ErrReadOnly", err)
	}
	if err := ro.DropCollection(key); !errors.Is(err, ErrReadOnly) {
		t.Errorf("DropCollection = %v, want ErrReadOnly", err)
	}
	ro.Close()

	// The writer was never blocked or disturbed.
	if err := col.Add("k3", "tag", "v3"); err != nil {
		t.Fatalf("writer after a read-only reader: %v", err)
	}
	rw.Close()
}

// TestReadOnlyMissingFileCreatesNothing: opening an absent collection
// read-only is an empty snapshot, not a new database.
func TestReadOnlyMissingFileCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	key := CollectionKey{Database: dir, Collection: "absent"}

	db := NewDB(true)
	db.SetReadOnly(true)
	col := db.GetCollection(key)
	if _, ok := col.Get("k", "rel"); ok {
		t.Error("a missing read-only collection answered a value")
	}
	if err := col.Add("k", "rel", "v"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Add on a missing read-only collection = %v", err)
	}
	db.Close()

	if _, err := os.Stat(filepath.Join(dir, "absent.tie")); !os.IsNotExist(err) {
		t.Errorf("read-only open created the database: %v", err)
	}
}
