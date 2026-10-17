package tiedb

import (
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// faultyFile wraps a dbFile and fails every write with ENOSPC after a mark,
// standing in for a full disk or a dying device (the review's risk-map
// guard-rail, without needing a loop-mounted filesystem).
type faultyFile struct {
	inner dbFile
	fail  bool
}

func (f *faultyFile) WriteAt(p []byte, off int64) (int, error) {
	if f.fail {
		return 0, syscall.ENOSPC
	}
	return f.inner.WriteAt(p, off)
}

func (f *faultyFile) ReadAt(p []byte, off int64) (int, error) { return f.inner.ReadAt(p, off) }
func (f *faultyFile) Sync() error                             { return f.inner.Sync() }
func (f *faultyFile) Close() error                            { return f.inner.Close() }

// TestWriteFailureIsStickyNotFatal is the regression for §4.6: the writer
// panicked on any write error — including ENOSPC — and the panic was on the
// writer goroutine, so it killed the whole triplestore (every other collection,
// every in-flight request) and lost the acknowledged writes still queued.
// A failed write now puts just that collection read-only: reads keep working,
// later writes report the error, and the process survives.
func TestWriteFailureIsStickyNotFatal(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, "enospc"})

	col.Add("k1", "rel", "v1")
	col.Sync()
	if err := col.Err(); err != nil {
		t.Fatalf("healthy collection reports an error: %v", err)
	}

	faulty := &faultyFile{inner: col.getFile(), fail: true}
	col.setFile(faulty)

	col.Add("k2", "rel", "v2") // queued; the writer is the one that fails
	col.Sync()                 // the barrier runs after the failed write

	if err := col.Err(); err == nil {
		t.Fatal("the writer did not record the I/O failure")
	}
	if err := col.Add("k3", "rel", "v3"); err == nil {
		t.Error("Add must report the collection's sticky I/O error")
	}
	if _, err := col.Apply([]Op{{Kind: OpAdd, Key: "k4", Relation: "rel", Values: []string{"v"}}}); err == nil {
		t.Error("Apply must report the collection's sticky I/O error")
	}

	// Reads still answer from the index — the collection is read-only, not gone.
	if _, found := col.Get("k1", "rel"); !found {
		t.Error("reads must keep working after a write failure")
	}

	// Other collections are unaffected.
	other := db.GetCollection(CollectionKey{dir, "healthy"})
	if err := other.Add("k", "rel", "v"); err != nil {
		t.Errorf("a healthy collection was affected: %v", err)
	}
	other.Sync()
	if err := other.Err(); err != nil {
		t.Errorf("a healthy collection was marked read-only: %v", err)
	}
}

// TestReloadAfterTruncation is the crash-injection guard from the risk map:
// cut the record file at a random record boundary, reload, and require the
// collection to come back consistent — no panic, no index/file divergence.
func TestReloadAfterTruncation(t *testing.T) {
	dir := t.TempDir()
	name := "trunc"
	path := filepath.Join(dir, name+".tie")

	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, name})
	for i := 0; i < 500; i++ {
		col.Add("subject-"+strconv.Itoa(i/5), "rel-"+strconv.Itoa(i%5), "value-"+strconv.Itoa(i))
	}
	col.Sync()
	db.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	records := fi.Size() / ENTRY_SIZE
	if records < 2 {
		t.Fatalf("expected a populated file, got %d records", records)
	}
	cut := ENTRY_SIZE * int64(1+rand.Intn(int(records)-1))
	if err := os.Truncate(path, cut); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	db2 := NewDB(true)
	col2 := db2.GetCollection(CollectionKey{dir, name})
	if err := col2.Err(); err != nil {
		t.Fatalf("reload after truncation failed: %v", err)
	}
	// The reloaded index must agree with what survived on the file.
	rep := col2.CheckIndex(IndexCheckOptions{Deep: true, SampleLimit: 5})
	if rep.Problems() > 0 {
		t.Errorf("reloaded index inconsistent after truncation at %d/%d records: %+v", cut/ENTRY_SIZE, records, rep)
		for i, s := range rep.Samples {
			if i >= 3 {
				break
			}
			t.Logf("  %s: %q %q %q @%d", s.Kind, s.Key, s.Value1, s.Value2, s.Position)
		}
	}
	// And it must still accept writes.
	if err := col2.Add("after", "rel", "crash"); err != nil {
		t.Errorf("write after reload failed: %v", err)
	}
	col2.Sync()
}
