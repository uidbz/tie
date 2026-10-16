package tiedb

import (
	"fmt"
	"testing"
)

// TestRelationFilterIsExact pins the filter semantics the id-based fast path
// must keep: an exact relation, nothing for a relation no triple carries, and
// not even a prefix of an interned relation.
func TestRelationFilterIsExact(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "filter"})

	const others = 50
	for i := 0; i < others; i++ {
		if err := col.Add("hub", fmt.Sprintf("rel-%02d", i), fmt.Sprintf("v-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"hit-1", "hit-2"} {
		if err := col.Add("hub", "target", v); err != nil {
			t.Fatal(err)
		}
	}

	set, found := col.Get("hub", "target")
	if !found {
		t.Fatal("Get(hub, target) found nothing")
	}
	if got := len(set["hub"]["target"]); got != 2 {
		t.Errorf("Get(hub, target) holds %d values, want 2", got)
	}

	// Get's bool says the key exists, not that the filter matched, so the
	// answer to check is the set: a relation no triple carries, and a prefix of
	// an interned relation, both come back empty.
	if set, _ := col.Get("hub", "no-such-relation"); len(set) != 0 {
		t.Errorf("a relation no triple carries returned %v", set)
	}
	if set, _ := col.Get("hub", "tar"); len(set) != 0 {
		t.Errorf("a prefix of an interned relation matched: %v", set)
	}
	// The empty filter still means every relation.
	set, _ = col.Get("hub", "")
	if got := len(set["hub"]); got != others+1 {
		t.Errorf("the empty filter returned %d relations, want %d", got, others+1)
	}
}

// TestRelationFilterOnReverseSet covers the other direction: the reverse
// association set of a value, filtered to one relation, must answer with the
// subjects that reference it through that relation only.
func TestRelationFilterOnReverseSet(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "filter-rev"})

	for i := 0; i < 20; i++ {
		if err := col.Add("subj"+fmt.Sprint(i), "parent", "dir"); err != nil {
			t.Fatal(err)
		}
	}
	if err := col.Add("other", "tag", "dir"); err != nil {
		t.Fatal(err)
	}

	rev, found := col.GetReverseAssociations("dir")
	if !found {
		t.Fatal("no reverse associations for dir")
	}
	set, total := col.GetTripleSet(rev, "parent", SortOptions{Limit: -1})
	if total != 20 {
		t.Errorf("reverse filter parent: total = %d, want 20", total)
	}
	if _, ok := set["other"]; ok {
		t.Error("the tag reference leaked into a parent-filtered answer")
	}
	if set, total = col.GetTripleSet(rev, "nope", SortOptions{Limit: -1}); total != 0 || len(set) != 0 {
		t.Errorf("reverse filter nope: total = %d, set = %d, want empty", total, len(set))
	}
}
