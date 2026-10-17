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
	if got := len(set); got != 2 {
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
	// The empty filter still means every relation: here, one value per rel-XX
	// plus the two under target.
	set, _ = col.Get("hub", "")
	if got := len(set); got != others+2 {
		t.Errorf("the empty filter returned %d values, want %d", got, others+2)
	}
}

// TestRelationValuesIsForwardOnly pins what the SortByValue lookup may read:
// the key's own values under one relation, nothing else. A value that only
// points back at the key (a child's parent edge) is not one of the key's
// values, and a relation the key does not carry is not found.
func TestRelationValuesIsForwardOnly(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "rel-values"})

	if err := col.Add("dir", "tie-type", "directory"); err != nil {
		t.Fatal(err)
	}
	for _, child := range []string{"a", "b", "c"} {
		if err := col.Add(child, "parent", "dir"); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"2001", "1999", "2000"} {
		if err := col.Add("dir", "year", v); err != nil {
			t.Fatal(err)
		}
	}

	values, found := col.relationValues("dir", "year")
	if !found {
		t.Fatal("dir carries no year values")
	}
	if len(values) != 3 {
		t.Errorf("year values = %v, want 3", values)
	}
	// The children's parent edges are not dir's own values.
	if values, found := col.relationValues("dir", "parent"); found {
		t.Errorf("dir has parent values %v, want none", values)
	}
	if _, found := col.relationValues("dir", "nope"); found {
		t.Error("a relation dir does not carry was found")
	}
	// The numeric minimum, not the lexicographic one, and not a value that
	// happens to parse.
	if n, ok := col.firstNumberOf("dir", "year"); !ok || n != 1999 {
		t.Errorf("firstNumberOf(dir, year) = %v, %v, want 1999", n, ok)
	}
	if v := col.firstValueOf("dir", "year"); v != "1999" {
		t.Errorf("firstValueOf(dir, year) = %q, want 1999", v)
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
	set, total := col.GetPage(rev, "parent", SortOptions{Limit: -1})
	if total != 20 {
		t.Errorf("reverse filter parent: total = %d, want 20", total)
	}
	for _, tr := range set {
		if tr.Key == "other" {
			t.Error("the tag reference leaked into a parent-filtered answer")
		}
	}
	if set, total = col.GetPage(rev, "nope", SortOptions{Limit: -1}); total != 0 || len(set) != 0 {
		t.Errorf("reverse filter nope: total = %d, set = %d, want empty", total, len(set))
	}
}
