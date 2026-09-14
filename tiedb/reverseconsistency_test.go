package tiedb

import (
	"testing"
)

// A subject whose every triple was deleted must vanish from reverse queries:
// every reverse-index entry must resolve to a live, correctly-addressed
// triple. Anything else is a "phantom" — a subject with no forward triples
// that still shows up in reverse-seeded result sets (observed in the wild as
// verify/fsck entries for fully-deleted subjects, scar tissue from the
// 98fe9db read-before-write era on long-running servers).
//
// Memory-only mode exercises the in-tree delete path; the disk-mode reload
// variant additionally proves the on-disk tombstone + rebuild-at-load path,
// which is what makes a server restart the cure for phantoms.
func TestReverseConsistencyAfterDelete(t *testing.T) {
	for _, disk := range []bool{false, true} {
		var dir string
		if disk {
			dir = t.TempDir()
		}
		db := NewDB(disk)
		col := db.GetCollection(CollectionKey{dir, "revcon"})
		col.Add("subj", "tie-type", "file")
		col.Add("subj", "parent", "dirA")
		col.Add("other", "tie-type", "file")

		if _, ok := col.Delete("subj", "tie-type", "file"); !ok {
			t.Fatal("delete tie-type failed")
		}
		if _, ok := col.Delete("subj", "parent", "dirA"); !ok {
			t.Fatal("delete parent failed")
		}
		if disk {
			col.Sync()
			db.Close()
			db = NewDB(disk)
			col = db.GetCollection(CollectionKey{dir, "revcon"})
		}
		defer db.Close()

		set, found := col.GetReverseAssociations("file")
		if !found {
			t.Fatal("no reverse set for 'file'")
		}
		n := 0
		clean := true
		set.ForEach(func(ua UniqueAssociation, pos int64) {
			n++
			tr, ok := col.resolveTriple(pos)
			if !ok {
				clean = false
				t.Error("reverse entry does not resolve")
				return
			}
			st := col.makeStringTriple(tr)
			if st.Key == "subj" || st.Value1 != "tie-type" || st.Value2 != "file" {
				clean = false
				t.Errorf("phantom/misresolved entry: %q %q %q", st.Key, st.Value1, st.Value2)
			}
		})
		if !clean {
			t.Errorf("disk=%v: reverse index inconsistent after full delete", disk)
		}
		if n != 1 {
			t.Errorf("disk=%v: expected 1 reverse entry for 'file', got %d", disk, n)
		}
	}
}
