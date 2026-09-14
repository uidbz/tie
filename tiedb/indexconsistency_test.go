package tiedb

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

// requireConsistent runs CheckIndex and fails the test on any divergence.
func requireConsistent(t *testing.T, col *Collection, deep bool, what string) IndexReport {
	t.Helper()
	rep := col.CheckIndex(IndexCheckOptions{Deep: deep})
	if rep.Problems() > 0 {
		t.Errorf("%s: index inconsistent: missingReverse=%d reverseOnly=%d posMismatch=%d misresolved=%d (fwd=%d rev=%d)",
			what, rep.MissingReverse, rep.ReverseOnly, rep.PositionMismatch, rep.Misresolved, rep.ForwardEntries, rep.ReverseEntries)
		for i, s := range rep.Samples {
			if i >= 5 {
				break
			}
			t.Logf("  %s: %q %q %q @%d", s.Kind, s.Key, s.Value1, s.Value2, s.Position)
		}
	}
	return rep
}

// TestConcurrentAddIndexConsistency pins the fix for the putAssoc lost-update:
// the first two triples of a fresh subject (forward outer key) or the first two
// children of a fresh directory (reverse outer key), inserted concurrently, must
// both survive in both indexes. Before the fix, the second creator of the outer
// set overwrote the first one's set and silently dropped its entry.
func TestConcurrentAddIndexConsistency(t *testing.T) {
	for _, disk := range []bool{false, true} {
		var dir string
		if disk {
			dir = t.TempDir()
		}
		db := NewDB(disk)
		col := db.GetCollection(CollectionKey{dir, "conc"})

		const subjects = 2000
		var wg sync.WaitGroup
		for i := 0; i < subjects; i++ {
			subj := "subj" + strconv.Itoa(i)
			parent := "dir" + strconv.Itoa(i/2) // two children per fresh dir
			// Two goroutines race to create the subject's forward set; two
			// sibling subjects race to create the parent's reverse set.
			wg.Add(2)
			go func() { defer wg.Done(); col.Add(subj, "tie-type", "file") }()
			go func() { defer wg.Done(); col.Add(subj, "parent", parent) }()
		}
		wg.Wait()
		col.Sync()

		rep := requireConsistent(t, col, disk, fmt.Sprintf("disk=%v concurrent add", disk))
		if rep.ForwardEntries != 2*subjects {
			t.Errorf("disk=%v: forward entries = %d, want %d (lost forward inserts)", disk, rep.ForwardEntries, 2*subjects)
		}
		if rep.ReverseEntries != 2*subjects {
			t.Errorf("disk=%v: reverse entries = %d, want %d (lost reverse inserts)", disk, rep.ReverseEntries, 2*subjects)
		}
		db.Close()
	}
}

// TestLoadIndexConsistency pins the same lost-update on the 8-worker load
// path: a subject's records are adjacent on disk and get dispatched to
// different workers, which race to create its forward set; children of one
// directory race to create the directory's reverse set.
func TestLoadIndexConsistency(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, "load"})

	const subjects = 20000
	for i := 0; i < subjects; i++ {
		subj := "subj" + strconv.Itoa(i)
		col.Add(subj, "tie-type", "file")
		col.Add(subj, "parent", "dir"+strconv.Itoa(i/4))
	}
	col.Sync()
	requireConsistent(t, col, false, "before reload")
	db.Close()

	db = NewDB(true)
	defer db.Close()
	col = db.GetCollection(CollectionKey{dir, "load"})
	rep := requireConsistent(t, col, false, "after reload")
	if rep.ForwardEntries != 2*subjects {
		t.Errorf("forward entries after reload = %d, want %d", rep.ForwardEntries, 2*subjects)
	}
	if rep.ReverseEntries != 2*subjects {
		t.Errorf("reverse entries after reload = %d, want %d", rep.ReverseEntries, 2*subjects)
	}
}

// TestConcurrentDuplicateAdd pins atomic dedup in Add: the same triple added
// from two goroutines must produce exactly one on-disk record, so a later
// Delete + reload does not resurrect it from the second record.
func TestConcurrentDuplicateAdd(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, "dup"})

	const n = 500
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		v := "tag" + strconv.Itoa(i)
		wg.Add(2)
		go func() { defer wg.Done(); col.Add("tags", "all", v) }()
		go func() { defer wg.Done(); col.Add("tags", "all", v) }()
	}
	wg.Wait()
	col.Sync()
	requireConsistent(t, col, true, "after duplicate adds")
	for i := 0; i < n; i++ {
		if _, ok := col.Delete("tags", "all", "tag"+strconv.Itoa(i)); !ok {
			t.Fatalf("delete tag%d failed", i)
		}
	}
	col.Sync()
	db.Close()

	db = NewDB(true)
	defer db.Close()
	col = db.GetCollection(CollectionKey{dir, "dup"})
	if got := getVal2s(t, col, "tags", "all"); len(got) != 0 {
		t.Errorf("%d triple(s) resurrected after delete+reload (duplicate on-disk records): %v", len(got), firstN(got, 5))
	}
	requireConsistent(t, col, true, "after reload")
}

// TestDeleteClearsReverseResidue: a reverse-only entry (forward lost) must be
// removable through Delete, so a phantom can be cleared without a restart.
func TestDeleteClearsReverseResidue(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"", "residue"})
	col.Add("subj", "tie-type", "file")
	col.Add("other", "tie-type", "file")

	// Simulate the lost forward entry.
	fwd, _ := col.GetAssociations("subj")
	v1, _, _ := col.getEntryFromString("tie-type")
	v2, _, _ := col.getEntryFromString("file")
	fwd.Delete(UniqueAssociation{AssociateTo: v2, Relation: v1})

	rep := col.CheckIndex(IndexCheckOptions{})
	if rep.ReverseOnly != 1 {
		t.Fatalf("setup: expected 1 reverse-only entry, report %+v", rep)
	}
	if msg, ok := col.Delete("subj", "tie-type", "file"); !ok {
		t.Fatalf("Delete of reverse residue failed: %s", msg)
	}
	requireConsistent(t, col, false, "after residue delete")
	set, _ := col.GetReverseAssociations("file")
	if set.Size() != 1 {
		t.Errorf("reverse set for 'file' has %d entries, want 1", set.Size())
	}
}

// TestCheckIndexRepair: Repair restores a lost reverse entry and drops a
// reverse-only one, and the collection is consistent afterwards.
func TestCheckIndexRepair(t *testing.T) {
	for _, disk := range []bool{false, true} {
		var dir string
		if disk {
			dir = t.TempDir()
		}
		db := NewDB(disk)
		col := db.GetCollection(CollectionKey{dir, "repair"})
		col.Add("a", "parent", "d1")
		col.Add("b", "parent", "d1")
		col.Add("c", "tie-type", "file")
		col.Sync()

		parentID, _, _ := col.getEntryFromString("parent")
		aID, _, _ := col.getEntryFromString("a")
		d1ID, _, _ := col.getEntryFromString("d1")
		tieTypeID, _, _ := col.getEntryFromString("tie-type")
		fileID, _, _ := col.getEntryFromString("file")
		cID, _, _ := col.getEntryFromString("c")

		// Lose a's reverse entry under d1; lose c's forward entry.
		rev, _ := col.GetReverseAssociations("d1")
		rev.Delete(UniqueAssociation{AssociateTo: aID, Relation: parentID})
		fwd, _ := col.GetAssociations("c")
		fwd.Delete(UniqueAssociation{AssociateTo: fileID, Relation: tieTypeID})
		_ = d1ID
		_ = cID

		rep := col.CheckIndex(IndexCheckOptions{Deep: disk})
		if rep.MissingReverse != 1 || rep.ReverseOnly != 1 {
			t.Fatalf("disk=%v: expected 1 missing-reverse + 1 reverse-only, got %+v", disk, rep)
		}
		rep = col.CheckIndex(IndexCheckOptions{Deep: disk, Repair: true})
		if rep.Repaired != 2 {
			t.Errorf("disk=%v: repaired = %d, want 2", disk, rep.Repaired)
		}
		requireConsistent(t, col, disk, fmt.Sprintf("disk=%v after repair", disk))

		// a is back in reverse parent lookups; c's phantom is gone.
		_, _, total, _ := col.QueryTags(TagQuery{Include: []string{"d1"}, Reverse: true, Filter: "parent", Sort: SortOptions{Limit: -1}})
		if total != 2 {
			t.Errorf("disk=%v: reverse parent lookup of d1 returned %d, want 2", disk, total)
		}
		_, _, total, _ = col.QueryTags(TagQuery{Include: []string{"file"}, Reverse: true, Filter: "tie-type", Sort: SortOptions{Limit: -1}})
		if total != 0 {
			t.Errorf("disk=%v: phantom c still returned by reverse tie-type query (%d)", disk, total)
		}
		db.Close()
	}
}

// TestDeepCheckRewritesMisresolved: a forward entry pointing at a slot whose
// record no longer matches (tombstoned) is detected by Deep and rewritten.
func TestDeepCheckRewritesMisresolved(t *testing.T) {
	dir := t.TempDir()
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, "deep"})
	col.Add("x", "tag", "t1")
	col.Sync()

	tagID, _, _ := col.getEntryFromString("tag")
	t1ID, _, _ := col.getEntryFromString("t1")
	fwd, _ := col.GetAssociations("x")
	pos, _ := fwd.Get(UniqueAssociation{AssociateTo: t1ID, Relation: tagID})
	// Tombstone the record behind the index's back.
	col.dBWriteQueue <- FileMod{Mode: FILE_DELETE, Position: pos}
	col.cache.Evict(pos)

	rep := col.CheckIndex(IndexCheckOptions{Deep: true})
	if rep.Misresolved != 1 {
		t.Fatalf("expected 1 misresolved, got %+v", rep)
	}
	rep = col.CheckIndex(IndexCheckOptions{Deep: true, Repair: true})
	if rep.Repaired != 1 {
		t.Fatalf("repaired = %d, want 1", rep.Repaired)
	}
	col.Sync()
	requireConsistent(t, col, true, "after rewrite")
	if got := getVal2s(t, col, "x", "tag"); !got["t1"] {
		t.Errorf("t1 not readable after rewrite: %v", got)
	}
	db.Close()

	db = NewDB(true)
	defer db.Close()
	col = db.GetCollection(CollectionKey{dir, "deep"})
	if got := getVal2s(t, col, "x", "tag"); !got["t1"] {
		t.Errorf("t1 lost after reload: %v", got)
	}
	requireConsistent(t, col, true, "after rewrite+reload")
}

func firstN(m map[string]bool, n int) []string {
	out := make([]string, 0, n)
	for k := range m {
		if len(out) == n {
			break
		}
		out = append(out, k)
	}
	return out
}
