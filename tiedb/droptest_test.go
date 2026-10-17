package tiedb

import (
	"testing"
	"time"
)

// TestDroppedCollectionDoesNotHang is the regression for §4.5: DropCollection
// closed the writer and forgot the collection, but a request that already held
// the *Collection kept using it — its reads sent to dBReadQueue with nobody to
// serve them and waited for a reply forever, holding a MaxConcurrentRequests
// slot; its writes parked on a queue nobody drained. `restore --drop` during
// any other activity triggered it.
func TestDroppedCollectionDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(true)
	key := CollectionKey{dir, "drop-hang"}
	col := db.GetCollection(key)

	col.Add("k", "rel", "v")
	col.Sync()

	var pos int64 = -1
	if set, found := col.GetAssociations("k"); found {
		set.ForEach(func(_ UniqueAssociation, p int64) { pos = p })
	}
	if pos < 0 {
		t.Fatal("no on-disk position recorded for the triple")
	}

	if err := db.DropCollection(key); err != nil {
		t.Fatalf("drop: %v", err)
	}

	// A disk read through the dropped collection must fail, not hang.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		if _, ok := col.readTripleAt(pos); ok {
			t.Error("read from a dropped collection reported success")
		}
	}()
	select {
	case <-readDone:
	case <-time.After(3 * time.Second):
		t.Fatal("readTripleAt hung after DropCollection")
	}

	// Writes and Sync through it must return, not park on a dead queue.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		col.Add("k2", "rel", "v2")
		col.SetValues("k3", "rel", []string{"a"})
		col.Sync()
	}()
	select {
	case <-writeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Add/SetValues/Sync hung after DropCollection")
	}

	// A fresh request resolves a new, empty collection rather than the corpse.
	fresh := db.GetCollection(key)
	if _, found := fresh.Get("k", "rel"); found {
		t.Error("dropped collection data resurfaced")
	}
}
