package tiedb

import (
	"fmt"
	"sync"
	"testing"
)

func mkdirSpec(newUID string) DirSpec {
	return DirSpec{
		Path:           "tie:/a/b",
		NewUID:         newUID,
		ParentUID:      "parent-uid",
		PathRelation:   "path",
		ParentRelation: "parent",
		TypeRelation:   "tie-type",
		TypeValue:      "directory",
	}
}

// TestMkDirIsAtomic is the §4.10 guard: the client used to ask "is there a UID
// for this path?", see none, mint one and write it. Two concurrent creators —
// two imports, a FUSE mkdir racing an import — both saw "none" and both wrote,
// which is where the duplicate-path state comes from.
func TestMkDirIsAtomic(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "mkdir-atomic"})

	const racers = 8
	var wg sync.WaitGroup
	uids := make([]string, racers)
	existed := make([]bool, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uid, ex, err := col.MkDir(mkdirSpec(fmt.Sprintf("uid-%d", i)))
			if err != nil {
				t.Errorf("racer %d: %v", i, err)
				return
			}
			uids[i] = uid
			existed[i] = ex
		}(i)
	}
	wg.Wait()

	// Every caller ends up with the same node.
	for i, uid := range uids {
		if uid != uids[0] {
			t.Fatalf("racer %d got %q, racer 0 got %q: two nodes for one path", i, uid, uids[0])
		}
	}
	created := 0
	for _, ex := range existed {
		if !ex {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d racers reported creating the node, want exactly 1", created)
	}

	// And the store holds exactly one subject claiming the path.
	owners, found := col.GetReverse("tie:/a/b", "path")
	if !found || len(owners) != 1 {
		t.Fatalf("%d subjects claim tie:/a/b, want 1", len(owners))
	}
	if !owners.Has(uids[0]) {
		t.Errorf("the owner is %v, want %s", owners, uids[0])
	}
	// The winner's node is complete: parent edge, path and type marker.
	for relation, want := range map[string]string{"parent": "parent-uid", "tie-type": "directory"} {
		if !hasValue(col, uids[0], relation, want) {
			t.Errorf("the created node is missing (%s, %s, %s)", uids[0], relation, want)
		}
	}
	// The losers' UIDs must not exist at all.
	for i := 0; i < racers; i++ {
		if uids[i] == uids[0] {
			continue
		}
		if set, ok := col.Get(uids[i], "path"); ok && len(set) > 0 {
			t.Errorf("loser uid-%d still wrote triples", i)
		}
	}
}

// TestMkDirSecondCallReportsExisted: the caller must learn the path was taken
// and get its owner, without a second round trip.
func TestMkDirSecondCallReportsExisted(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "mkdir-existed"})

	first, existed, err := col.MkDir(mkdirSpec("uid-first"))
	if err != nil || existed {
		t.Fatalf("first MkDir = (%q, %v, %v)", first, existed, err)
	}
	second, existed, err := col.MkDir(mkdirSpec("uid-second"))
	if err != nil {
		t.Fatal(err)
	}
	if !existed || second != first {
		t.Errorf("second MkDir = (%q, %v), want (%q, true)", second, existed, first)
	}
}

// TestMkDirValidatesItsSpec: a spec without a path or a relation cannot create
// an unusable node.
func TestMkDirValidatesItsSpec(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "mkdir-spec"})

	if _, _, err := col.MkDir(DirSpec{NewUID: "u", PathRelation: "path"}); err == nil {
		t.Error("a spec with no path was accepted")
	}
	if _, _, err := col.MkDir(DirSpec{Path: "/a", PathRelation: "path"}); err == nil {
		t.Error("a spec with no new uid was accepted")
	}
	if _, _, err := col.MkDir(DirSpec{Path: "/a", NewUID: "u"}); err == nil {
		t.Error("a spec with no path relation was accepted")
	}
}

// hasValue checks one triple in the forward direction: TripleSet is keyed by
// subject, then relation, then value.
func hasValue(col *Collection, key, relation, value string) bool {
	set, found := col.Get(key, relation)
	if !found {
		return false
	}
	rels, ok := set[key]
	if !ok {
		return false
	}
	vals, ok := rels[relation]
	return ok && vals.Has(value)
}
