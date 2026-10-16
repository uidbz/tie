package tiedb

import (
	"sort"
	"testing"
)

// cotagsFixture: s1 carries x+y and a kind, s2 carries x+z, s3 carries y.
func cotagsFixture(t *testing.T) *Collection {
	t.Helper()
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "cotags"})
	triples := [][3]string{
		{"s1", "tag", "x"}, {"s1", "tag", "y"}, {"s1", "kind", "dog"},
		{"s2", "tag", "x"}, {"s2", "tag", "z"},
		{"s3", "tag", "y"},
	}
	for _, tr := range triples {
		if err := col.Add(tr[0], tr[1], tr[2]); err != nil {
			t.Fatal(err)
		}
	}
	return col
}

func coTagsSorted(col *Collection, q TagQuery, relation string) []string {
	tags, _ := col.CoTags(q, relation)
	sort.Strings(tags)
	return tags
}

// TestCoTagsUsesTheSameAlgebraAsQueryTags pins §8 S6: QueryTags and CoTags are
// one algebra over different answers, so anything that narrows the match set
// must narrow both. CoTags carried its own copy of the algebra and had already
// drifted — it ignored MissingRelation.
func TestCoTagsUsesTheSameAlgebraAsQueryTags(t *testing.T) {
	col := cotagsFixture(t)

	if got := coTagsSorted(col, TagQuery{Include: []string{"x"}, Reverse: true}, "tag"); len(got) != 3 || got[0] != "x" || got[1] != "y" || got[2] != "z" {
		t.Errorf("CoTags(x) = %v, want [x y z]", got)
	}
	if got := coTagsSorted(col, TagQuery{Include: []string{"x"}, Exclude: []string{"y"}, Reverse: true}, "tag"); len(got) != 2 || got[0] != "x" || got[1] != "z" {
		t.Errorf("CoTags(x, not y) = %v, want [x z]", got)
	}

	// The one the copies disagreed on: of the subjects tagged x, keep those
	// with no "kind" triple — that is s2 alone.
	missing := TagQuery{Include: []string{"x"}, Reverse: true, MissingRelation: "kind"}
	_, sorted, total, found := col.QueryTags(missing)
	if !found || total != 1 || len(sorted) != 1 || sorted[0].Key != "s2" {
		t.Errorf("QueryTags(x, missing kind) = %v/%d/%v, want s2", sorted, total, found)
	}
	if got := coTagsSorted(col, missing, "tag"); len(got) != 2 || got[0] != "x" || got[1] != "z" {
		t.Errorf("CoTags(x, missing kind) = %v, want [x z]", got)
	}

	// The scope term narrows both the same way: only s1 carries kind=dog.
	scoped := TagQuery{Include: []string{"x"}, Reverse: true, Scope: "dog"}
	if _, _, total, _ := col.QueryTags(scoped); total != 1 {
		t.Errorf("QueryTags(x, scope dog) total = %d, want 1", total)
	}
	if got := coTagsSorted(col, scoped, "tag"); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("CoTags(x, scope dog) = %v, want [x y]", got)
	}
}

// TestCoTagsReturnConventions: the seed absent and the unmet AND term are
// different answers, and callers rely on them.
func TestCoTagsReturnConventions(t *testing.T) {
	col := cotagsFixture(t)

	if tags, ok := col.CoTags(TagQuery{Include: []string{"nope"}, Reverse: true}, "tag"); tags != nil || ok {
		t.Errorf("absent seed = (%v, %v), want (nil, false)", tags, ok)
	}
	if tags, ok := col.CoTags(TagQuery{Include: []string{"x", "nope"}, Reverse: true}, "tag"); tags != nil || !ok {
		t.Errorf("unmet AND term = (%v, %v), want (nil, true)", tags, ok)
	}
	if tags, ok := col.CoTags(TagQuery{Include: []string{"x"}, Reverse: true, Scope: "nope"}, "tag"); tags != nil || !ok {
		t.Errorf("absent scope = (%v, %v), want (nil, true)", tags, ok)
	}
	if tags, ok := col.CoTags(TagQuery{Include: []string{"x"}, Reverse: true}, "no-such-relation"); tags != nil || !ok {
		t.Errorf("absent tag relation = (%v, %v), want (nil, true)", tags, ok)
	}
}
