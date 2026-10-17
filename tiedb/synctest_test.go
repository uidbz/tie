package tiedb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// recordTypes returns the TYPE_* value of every ENTRY_SIZE record in the file.
func recordTypes(t *testing.T, path string) []uint16 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading db file: %v", err)
	}
	var out []uint16
	for off := 0; off+ENTRY_SIZE <= len(data); off += ENTRY_SIZE {
		out = append(out, binary.LittleEndian.Uint16(data[off:off+2]))
	}
	return out
}

// TestSyncBarrierFlushesDeletes is the regression for §4.2(4): the old
// WaitGroup counted only FILE_ADD, so Sync returned while a Delete's
// tombstone was still queued. Sync is now a FIFO barrier: after it returns,
// every write queued before it — deletes included — is on the file.
func TestSyncBarrierFlushesDeletes(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, "barrier"})

	col.Add("k", "rel", "v")
	col.Sync()

	path := filepath.Join(dir, "barrier.tie")
	before := recordTypes(t, path)
	var assocs int
	for _, ty := range before {
		if ty == TYPE_ASSOCIATION {
			assocs++
		}
	}
	if assocs == 0 {
		t.Fatal("no association record on file after Add+Sync")
	}
	for _, ty := range before {
		if ty == TYPE_DELETE {
			t.Fatal("tombstone present before Delete")
		}
	}

	if _, ok := col.Delete("k", "rel", "v"); !ok {
		t.Fatal("delete failed")
	}
	col.Sync()

	var tombstones int
	for _, ty := range recordTypes(t, path) {
		if ty == TYPE_DELETE {
			tombstones++
		}
	}
	if tombstones == 0 {
		t.Error("Sync returned before the queued delete tombstone reached the file")
	}
}

// TestSyncBarrierNoStarvation: the old Sync waited for EVERY client's writes,
// so a steady stream of Adds from elsewhere could keep it waiting forever.
// The barrier only waits for what was queued when Sync was called.
func TestSyncBarrierNoStarvation(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, "starve"})

	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			col.Add("k", "rel", "v"+strconv.Itoa(i))
		}
	}()

	done := make(chan struct{})
	go func() {
		col.Sync()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Sync starved behind a continuous stream of other writes")
	}
	close(stop)
}

// TestSyncAfterCloseDoesNotHang: a barrier queued against a writer that has
// already exited must not block forever.
func TestSyncAfterCloseDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, "closed"})
	col.Add("k", "rel", "v")
	col.Sync()
	col.closeDB()

	done := make(chan struct{})
	go func() {
		col.Sync()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Sync hung after the collection was closed")
	}
}
