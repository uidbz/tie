package tiedb

import "testing"

// TestGetForwardOnlySelfReference is the regression for §4.14: Get merged the
// reverse triples of the relation into the result keyed by the other subject.
// For a self-referencing triple — the root directory's own parent edge — the
// merge overwrote data[key] with the reverse set, so SetValues/SimpleUpdate
// ("replace parent on root") operated on the wrong value set.
func TestGetForwardOnlySelfReference(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "selfref"})

	col.Add("root", "parent", "root")  // self-reference: root is its own parent
	col.Add("child", "parent", "root") // reverse index for root: child points at it

	row, found := col.Get("root", "parent")
	if !found {
		t.Fatal("Get: forward triple not found")
	}
	if len(row) != 1 {
		t.Errorf("Get returned %d subjects %v, want only the forward subject 'root'", len(row), row)
	}
	vals := row
	if len(vals) != 1 {
		t.Fatalf("forward set = %v, want exactly {root}", vals)
	}

	// The other direction is available explicitly.
	rev, found := col.GetReverse("root", "parent")
	if !found {
		t.Fatal("GetReverse: nothing found")
	}
	if !holdsValue(rev, "child") {
		t.Errorf("GetReverse = %v, want it to include 'child'", rev)
	}

	// The write primitive built on Get must see (and replace) only the forward
	// set, and must leave the reverse edge alone.
	if _, ok := col.SimpleUpdate("root", "parent", "newparent", false); !ok {
		t.Fatal("SimpleUpdate failed")
	}
	row, _ = col.Get("root", "parent")
	if len(row) != 1 {
		t.Errorf("root's parent set = %v, want exactly one value", row)
	}
	if !holdsValue(row, "newparent") {
		t.Errorf("root's parent set = %v, want {newparent}", row)
	}
	row, _ = col.Get("child", "parent")
	if !holdsValue(row, "root") {
		t.Errorf("child's parent edge was clobbered: %v", row)
	}
}
