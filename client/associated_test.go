package client

import (
	"sort"
	"testing"
)

// TestAssociatedReturnsRows pins the reply shape after §8 S9: Associated answers
// with rows — one per subject associated with the key, carrying that subject's
// attributes — instead of the nested TripleSet map, whose JSON came out in a
// different order on every call and cost a map allocation per value.
func TestAssociatedReturnsRows(t *testing.T) {
	cfg := TestingConfig()
	cfg.Namespace = "assoctesting"
	cfg.Collection = "assoc-rows"
	cfg.DefaultFileHosts = []string{"default"}
	cfg.FileHosts = map[string]FileHost{"default": {URL: testingFileHostURL}}
	tc := NewTieClient(cfg)
	if err := tc.DropCollection(); err != nil {
		t.Fatalf("dropping the test collection: %v", err)
	}

	for _, tr := range [][3]string{
		{"song-a", "tag", "jazz"}, {"song-a", "name", "A"},
		{"song-b", "tag", "jazz"}, {"song-b", "name", "B"},
		{"song-c", "tag", "rock"},
	} {
		if _, err := tc.Add(tr[0], tr[1], tr[2]); err != nil {
			t.Fatalf("add %v: %v", tr, err)
		}
	}

	reply, err := tc.Associated("jazz")
	if err != nil {
		t.Fatalf("Associated: %v", err)
	}
	if len(reply.Rows) != 2 {
		t.Fatalf("Associated(jazz) returned %d rows, want 2: %+v", len(reply.Rows), reply.Rows)
	}

	got := map[string]string{}
	for _, row := range reply.Rows {
		got[row.Key] = RowFirst(row, RelName)
	}
	if got["song-a"] != "A" || got["song-b"] != "B" {
		t.Errorf("rows carry the wrong attributes: %+v", got)
	}

	// The same read through Query, which is the form to prefer: it paginates and
	// takes several terms. The two must agree on which subjects they name.
	rows, _, err := tc.Query(QuerySpec{Terms: []string{"jazz"}, Reverse: true, Expand: true, Limit: -1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	fromQuery := make([]string, 0, len(rows))
	for _, row := range rows {
		fromQuery = append(fromQuery, row.Key)
	}
	fromAssoc := make([]string, 0, len(reply.Rows))
	for _, row := range reply.Rows {
		fromAssoc = append(fromAssoc, row.Key)
	}
	sort.Strings(fromQuery)
	sort.Strings(fromAssoc)
	if len(fromQuery) != len(fromAssoc) || fromQuery[0] != fromAssoc[0] || fromQuery[1] != fromAssoc[1] {
		t.Errorf("Associated and Query{Reverse, Expand} disagree: %v vs %v", fromAssoc, fromQuery)
	}
}
