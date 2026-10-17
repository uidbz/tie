package tiedb

import (
	"sync"
	"testing"
)

// TestApplyAtomic: a batch that fails validation must leave NOTHING behind.
// The old per-op loop applied every op before the failing one.
func TestApplyAtomic(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "apply-atomic"})

	applied, err := col.Apply([]Op{
		{Kind: OpAdd, Key: "a", Relation: "rel", Values: []string{"1"}},
		{Kind: OpAdd, Key: "b", Relation: "rel", Values: []string{"2"}},
		{Kind: OpUpdate, Key: "missing", Relation: "rel", Values: []string{"x"}, NewValue: "y"},
	})
	if err == nil {
		t.Fatal("update of a missing triple must fail the batch")
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0", applied)
	}
	for _, k := range []string{"a", "b"} {
		if _, found := col.Get(k, "rel"); found {
			t.Errorf("op before the failure was still applied: %q", k)
		}
	}

	// Unknown kinds are rejected the same way.
	if _, err := col.Apply([]Op{{Kind: "frobnicate"}, {Kind: OpAdd, Key: "c", Relation: "rel", Values: []string{"1"}}}); err == nil {
		t.Error("unknown op kind must fail the batch")
	}
	if _, found := col.Get("c", "rel"); found {
		t.Error("op after an invalid one was applied")
	}
}

// TestApplyAddIfAbsent is the compare-and-set primitive MkDir needs: exactly
// one of N concurrent creators wins.
func TestApplyAddIfAbsent(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "apply-cas"})

	const racers = 8
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			col.Apply([]Op{{Kind: OpAddIfAbsent, Key: "/a/b", Relation: "path", Values: []string{"uid-" + string(rune('0'+i))}}})
		}(i)
	}
	wg.Wait()

	row, found := col.Get("/a/b", "path")
	if !found {
		t.Fatal("path triple missing")
	}
	n := len(row)
	if n != 1 {
		t.Errorf("path holds %d uids, want exactly 1 (the loser must not add)", n)
	}

	// A second add-if-absent against a now-populated relation is a no-op.
	col.Apply([]Op{{Kind: OpAddIfAbsent, Key: "/a/b", Relation: "path", Values: []string{"late"}}})
	row, _ = col.Get("/a/b", "path")
	n = len(row)
	if n != 1 {
		t.Errorf("add-if-absent wrote to a populated relation: %d values", n)
	}
}

// TestApplyOrdering: ops run in slice order, so a delete-then-add pair
// replaces a value inside one batch.
func TestApplyOrdering(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "apply-order"})
	col.Add("k", "rel", "old")

	if _, err := col.Apply([]Op{
		{Kind: OpDelete, Key: "k", Relation: "rel", Values: []string{"old"}},
		{Kind: OpAdd, Key: "k", Relation: "rel", Values: []string{"new"}},
	}); err != nil {
		t.Fatal(err)
	}
	row, _ := col.Get("k", "rel")
	if len(row) != 1 {
		t.Errorf("relation = %v, want exactly {new}", row)
	}
	if !holdsValue(row, "new") {
		t.Errorf("relation does not hold 'new': %v", row)
	}
}
