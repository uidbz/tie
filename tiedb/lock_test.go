//go:build unix

package tiedb

import (
	"strings"
	"testing"
)

// TestLockingExcludesASecondOpen is the corruption guard behind `tie -f`: while
// one tree holds a collection, a second tree must refuse to load it. It also
// proves the lock is released when the owner closes.
func TestLockingExcludesASecondOpen(t *testing.T) {
	dir := t.TempDir()
	key := CollectionKey{Database: dir, Collection: "locked"}

	first := NewDB(true)
	first.SetLocking(true)
	col, err := first.TryGetCollection(key)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := col.Add("k", "rel", "v"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	second := NewDB(true)
	second.SetLocking(true)
	if _, err := second.TryGetCollection(key); err == nil {
		t.Fatal("a second tree opened a locked collection")
	} else if !strings.Contains(err.Error(), "locked by another process") {
		t.Fatalf("second open error = %v, want a lock message", err)
	}
	second.Close()

	first.Close()

	third := NewDB(true)
	third.SetLocking(true)
	defer third.Close()
	col3, err := third.TryGetCollection(key)
	if err != nil {
		t.Fatalf("open after the owner closed: %v", err)
	}
	got, ok := col3.Get("k", "rel")
	if !ok || len(got) != 1 || got[0] != "v" {
		t.Fatalf("reopened data = %v, %v", got, ok)
	}
}

// TestLockingOffByDefault: the engine does not change behaviour unless a caller
// asks for locking (webservice.NewWebservice does; the unit tests and embedders
// that manage their own lifecycle do not).
func TestLockingOffByDefault(t *testing.T) {
	dir := t.TempDir()
	key := CollectionKey{Database: dir, Collection: "unlocked"}

	first := NewDB(true)
	if _, err := first.TryGetCollection(key); err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer first.Close()

	second := NewDB(true)
	if _, err := second.TryGetCollection(key); err != nil {
		t.Fatalf("second open with locking off: %v", err)
	}
	second.Close()
}
