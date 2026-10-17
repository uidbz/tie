package tiedb

import (
	"fmt"
	"strconv"
	"testing"
)

// pagedFixture makes a subject set big enough that a page of 1000 is a real
// page, and returns the reverse association set of the tag they share.
func pagedFixture(t *testing.T, matches int) (*Collection, *AssociationSet) {
	t.Helper()
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "paged"})
	for i := 0; i < matches; i++ {
		key := fmt.Sprintf("file%05d", i)
		if err := col.Add(key, "tag", "shared"); err != nil {
			t.Fatal(err)
		}
		if err := col.Add(key, "tie-type", "audio-file"); err != nil {
			t.Fatal(err)
		}
	}
	set, found := col.GetReverseAssociations("shared")
	if !found {
		t.Fatal("no reverse associations for the shared tag")
	}
	return col, set
}

// TestPagedQueryCoversEveryMatch: paging must partition the matches — every
// match exactly once, whatever order the pages come in.
func TestPagedQueryCoversEveryMatch(t *testing.T) {
	const matches = 5000
	col, set := pagedFixture(t, matches)

	seen := make(map[string]int, matches)
	var total int
	for offset := 0; offset < matches; offset += 1000 {
		page, tot := col.GetPage(set, "", SortOptions{Offset: offset, Limit: 1000})
		total = tot
		if len(page) != 1000 {
			t.Fatalf("page at %d holds %d rows, want 1000", offset, len(page))
		}
		for _, tr := range page {
			seen[tr.Key]++
		}
	}
	if total != matches {
		t.Errorf("total = %d, want %d", total, matches)
	}
	if len(seen) != matches {
		t.Errorf("the pages cover %d subjects, want %d", len(seen), matches)
	}
	for key, n := range seen {
		if n != 1 {
			t.Errorf("%s appears in %d pages, want exactly 1", key, n)
		}
	}
}

// TestPagedQueryIsStableAcrossRequests: two runs of the same paging sequence
// must agree, or a UI walking pages sees items twice or misses them.
func TestPagedQueryIsStableAcrossRequests(t *testing.T) {
	col, set := pagedFixture(t, 3000)

	first, _ := col.GetPage(set, "", SortOptions{Offset: 1000, Limit: 500})
	second, _ := col.GetPage(set, "", SortOptions{Offset: 1000, Limit: 500})
	if len(first) != len(second) {
		t.Fatalf("page sizes differ between runs: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("row %d differs between runs: %v then %v", i, first[i], second[i])
		}
	}
}

// TestSmallResultKeepsAlphabeticalOrder: when the page covers every match, the
// query is not really paginated, and the usual key ordering is kept — the fast
// path must not reshuffle the common `query --limit 1000` over a small result.
func TestSmallResultKeepsAlphabeticalOrder(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "paged-small"})
	for _, key := range []string{"delta", "alpha", "charlie", "bravo"} {
		if err := col.Add(key, "tag", "x"); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := col.GetReverseAssociations("x")

	page, total := col.GetPage(set, "", SortOptions{Limit: 1000})
	if total != 4 || len(page) != 4 {
		t.Fatalf("page = %d rows of %d, want 4", len(page), total)
	}
	want := []string{"alpha", "bravo", "charlie", "delta"}
	for i, w := range want {
		if page[i].Key != w {
			t.Errorf("row %d = %s, want %s (alphabetical)", i, page[i].Key, w)
		}
	}
}

// TestOrderedQueriesKeepTheirOrder: the fast path is only for queries that ask
// for no ordering. SortBy and SortByValue keep their documented ordering even
// when paginated.
func TestOrderedQueriesKeepTheirOrder(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "paged-ordered"})
	// Inserted in reverse alphabetical order, so insertion order and key order
	// differ, and a value ordering differs from both.
	for i, key := range []string{"zulu", "yankee", "xray", "whiskey", "victor"} {
		if err := col.Add(key, "tag", "x"); err != nil {
			t.Fatal(err)
		}
		if err := col.Add(key, "rank", strconv.Itoa(10-i)); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := col.GetReverseAssociations("x")

	page, _ := col.GetPage(set, "", SortOptions{Limit: 2, SortByValue: "rank", SortByValueNumeric: true})
	if len(page) != 2 || page[0].Key != "victor" || page[1].Key != "whiskey" {
		t.Errorf("value-ordered page = %v, want [victor whiskey]", pageKeys(page))
	}

	page, _ = col.GetPage(set, "", SortOptions{Limit: 2, SortBy: "tag"})
	if len(page) != 2 || page[0].Key != "victor" || page[1].Key != "whiskey" {
		t.Errorf("key-ordered page = %v, want [victor whiskey]", pageKeys(page))
	}
}

func pageKeys(triples []StringTriple) []string {
	out := make([]string, 0, len(triples))
	for _, tr := range triples {
		out = append(out, tr.Key)
	}
	return out
}

// TestPagedQueryOffsetBeyondEnd keeps the old answer for an offset past the
// last match: no rows, but the true total.
func TestPagedQueryOffsetBeyondEnd(t *testing.T) {
	col, set := pagedFixture(t, 1500)

	page, total := col.GetPage(set, "", SortOptions{Offset: 5000, Limit: 1000})
	if len(page) != 0 {
		t.Errorf("offset past the end returned %d rows, want 0", len(page))
	}
	if total != 1500 {
		t.Errorf("total = %d, want 1500", total)
	}
}
