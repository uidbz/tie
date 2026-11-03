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

// TestReadOnlyEveryWritePathRefuses pins #154: Apply, Update, UpdateAdd,
// SimpleUpdate and SetValues swallowed the helpers' ErrReadOnly and answered
// success (or (message, true)) while leaving the index untouched. The engine
// guarantee "writes return ErrReadOnly" has to hold on every public path.
func TestReadOnlyEveryWritePathRefuses(t *testing.T) {
	dir := t.TempDir()
	key := CollectionKey{Database: dir, Collection: "honest"}

	rw := NewDB(true)
	col := rw.GetCollection(key)
	if err := col.Add("k", "tag", "v"); err != nil {
		t.Fatal(err)
	}
	col.Sync()
	rw.Close()

	ro := NewDB(true)
	ro.SetReadOnly(true)
	defer ro.Close()
	rc, err := ro.TryGetCollection(key)
	if err != nil {
		t.Fatal(err)
	}

	if n, err := rc.Apply([]Op{{Kind: OpAdd, Key: "k", Relation: "tag", Values: []string{"v2"}}}); !errors.Is(err, ErrReadOnly) || n != 0 {
		t.Errorf("Apply = %d, %v; want 0, ErrReadOnly", n, err)
	}
	if msg, ok := rc.Update("k", "tag", "v", "v2"); ok {
		t.Errorf("Update = %q, true; want false", msg)
	}
	if msg, ok := rc.UpdateAdd("k", "tag", "v", "v2"); ok {
		t.Errorf("UpdateAdd = %q, true; want false", msg)
	}
	if msg, ok := rc.SimpleUpdate("k", "tag", "v2", true); ok {
		t.Errorf("SimpleUpdate = %q, true; want false", msg)
	}
	rc.SetValues("k", "tag", []string{"w1", "w2"})

	// Nothing changed: the index still holds exactly the original triple.
	if got, ok := rc.Get("k", "tag"); !ok || len(got) != 1 || got[0] != "v" {
		t.Errorf("index after refused writes = %v, %v; want exactly [v]", got, ok)
	}
}
