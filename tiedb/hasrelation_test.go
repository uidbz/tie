package tiedb

// Tests for #124: the relation-presence predicate. Both query directions key on
// a *value*, so "which subjects hold the relation path, whatever its value" — and
// its conjunction with "…and do not hold tie-type" — cannot be asked at all. That
// is the question that finds a directory node whose structural marker is gone and
// which no edge points at.

import (
	"testing"
)

func holdsRelationFixture(t *testing.T) *Collection {
	t.Helper()
	db := NewDB(true)
	db.SetDefaultReverseRelations([]string{"tie-type", "parent"})
	col := db.GetCollection(CollectionKey{t.TempDir(), "holds"})

	col.Add("dirA", "tie-type", "directory")
	col.Add("dirA", "path", "tie:/a")
	col.Add("dirB", "tie-type", "directory")
	col.Add("dirB", "path", "tie:/b")
	col.Add("fileC", "tie-type", "file")
	col.Add("fileC", "parent", "dirA")
	// A directory whose tie-type triple is gone. Nothing references it: no child
	// edge, no parent edge, so no edge-driven scan can stumble over it.
	col.Add("orphanD", "path", "tie:/d")
	col.Sync()
	return col
}

func TestQueryTagsHasRelation(t *testing.T) {
	col := holdsRelationFixture(t)

	// The question the store could not answer: every subject carrying the
	// relation, whatever its value.
	got := queryKeys(t, col, TagQuery{HasRelation: "path"})
	if !got["dirA"] || !got["dirB"] || !got["orphanD"] {
		t.Errorf("subjects holding path = %v, want dirA, dirB and orphanD", got)
	}
	if got["fileC"] {
		t.Errorf("subjects holding path = %v, but fileC holds no path", got)
	}

	// The conjunction verify needs: holds path, lacks the structural marker.
	got = queryKeys(t, col, TagQuery{HasRelation: "path", MissingRelation: "tie-type"})
	if len(got) != 1 || !got["orphanD"] {
		t.Errorf("path without tie-type = %v, want orphanD only", got)
	}

	// A predicate on a relation nothing carries matches nothing — the mirror of
	// MissingRelation, where an unknown relation matches everything.
	got = queryKeys(t, col, TagQuery{HasRelation: "no-such-relation"})
	if len(got) != 0 {
		t.Errorf("subjects holding an unknown relation = %v, want empty", got)
	}
}

// TestQueryTagsHasRelationNarrowsATerm: the predicate also narrows an existing
// seed, so one round trip answers "of these matches, which hold relation R".
func TestQueryTagsHasRelationNarrowsATerm(t *testing.T) {
	col := holdsRelationFixture(t)

	got := queryKeys(t, col, TagQuery{
		Include: []string{"directory"}, Reverse: true, HasRelation: "path",
	})
	if !got["dirA"] || !got["dirB"] || got["orphanD"] || got["fileC"] {
		t.Errorf("dir-typed subjects holding path = %v, want dirA and dirB", got)
	}

	// Both predicates at once: dir-typed, holds path, lacks a tag.
	got = queryKeys(t, col, TagQuery{
		Include: []string{"directory"}, Reverse: true, HasRelation: "path", MissingRelation: "tag",
	})
	if !got["dirA"] || !got["dirB"] || got["orphanD"] || got["fileC"] {
		t.Errorf("dir-typed, path-carrying, untagged = %v, want dirA and dirB", got)
	}

	// Adding the marker takes orphanD out of the marker-gap answer.
	col.Add("orphanD", "tie-type", "directory")
	col.Sync()
	got = queryKeys(t, col, TagQuery{HasRelation: "path", MissingRelation: "tie-type"})
	if len(got) != 0 {
		t.Errorf("after restoring the marker, path without tie-type = %v, want empty", got)
	}
}
