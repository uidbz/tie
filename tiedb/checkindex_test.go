package tiedb

import (
	"strings"
	"testing"
)

// TestCheckIndexDanglingIdNoPanic is the regression for the CheckIndex
// crash-on-report bug: getValue(0, id) dereferenced a nil UniqueValue, so
// sampling the very dangling ids the check exists to report nil-dereffed the
// handler. CheckIndex must report them, rendering unknown ids as "#<id>".
func TestCheckIndexDanglingIdNoPanic(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "dangling"})
	col.Add("real", "tag", "value")

	// Forge a forward triple whose relation and value2 ids have no trie
	// entry — exactly the dangling-id case.
	col.changeMutex.Lock()
	tree, found := col.GetAssociations("real")
	if !found {
		col.changeMutex.Unlock()
		t.Fatal("subject not found")
	}
	tree.Put(UniqueAssociation{AssociateTo: 987654321, Relation: 123456789}, 55555)
	col.changeMutex.Unlock()

	rep := col.CheckIndex(IndexCheckOptions{SampleLimit: 10}) // must not panic
	if rep.Misresolved == 0 {
		t.Error("dangling ids not reported as misresolved")
	}
	var sawHash bool
	for _, s := range rep.Samples {
		if strings.HasPrefix(s.Value1, "#") || strings.HasPrefix(s.Value2, "#") {
			sawHash = true
		}
	}
	if !sawHash {
		t.Errorf("samples do not render dangling ids as #<id>: %+v", rep.Samples)
	}

	// getValue itself is nil-safe at every level.
	if v := col.getValue(0, 987654321); v != nil {
		t.Errorf("getValue(0, dangling) = %v, want nil", v)
	}
	if v := col.getValue(3, 987654321); v != nil {
		t.Errorf("getValue(3, dangling) = %v, want nil", v)
	}
}
