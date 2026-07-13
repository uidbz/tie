package tiedb

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

//TODO: Rewrite tests

/*
func TestInsertAssociation(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"Collections", "test"})
	input1 := "/home/johan/gopath/src/nicecode.rocks/uid/tiedb"
	input2 := "/home/johan/gopath/src/nicecode.rocks/uid/tiedb2"

	col.Add("a", input1, "file")
	col.Add("a", input2, "file")

	found, asses := col.GetAssociations("a")

	if found {
		out, _ := col.SetToString("a", "", asses)
		if out.Value2[0] != input2 {
			t.Errorf("Error input1, got: %s, want: %s.", out.Value2[0], input2)
		}
		if out.Value2[1] != input1 {
			t.Errorf("Error input2, got: %s, want: %s.", out.Value2[1], input1)
		}
	} else {
		t.Error("Error GetAssociations, did not find", "a")
	}
}
// func TestInsertLongEntry(t *testing.T) {
// 	db := NewDB()
// 	col := db.GetCollection(CollectionKey{"Collections", "test2"})
// 	input1 := "/home/johan/gopath/src/nicecode.rocks/uid/tiedb"
// 	input2 := "/home/johan/gopath/src/nicecode.rocks/uid/tiedb2"

// 	e1 := col.Insert(input1)
// 	e2 := col.Insert(input2)

// 	ic := col.(*InternalCollection)

// 	val2 := ic.GetValueString(e2.Level, e2.Id)
// 	val1 := ic.GetValueString(e1.Level, e1.Id)

// 	if val1 != input1 {
// 		t.Errorf("Error input1, got: %s, want: %s.", val1, input1)
// 	}
// 	if val2 != input2 {
// 		t.Errorf("Error input2, got: %s, want: %s.", val2, input2)
// 	}
// }
*/

func TestAddAssociation(t *testing.T) {
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{"Collections", "test3"})

	col.Add("superkey", "value1", "value2-1")
	col.Add("superkey", "value1", "value2-2")
	// col.Delete("superkey", "value1-2", "value2-1")
	// col.Add("superkey", "value3", "file2")
	// col.Add("superkey", "value4", "file2")
	col.Sync()

	asses, found := col.GetAssociations("superkey")

	if found {
		out, _ := col.GetTripleSet(asses, "", SortOptions{Limit: -1})
		if !out["superkey"]["value1"].Has("value2-1") {
			t.Errorf("Error input1, didn't have %s", "value2-1")
		}
		if !out["superkey"]["value1"].Has("value2-2") {
			t.Errorf("Error input2, didn't have %s", "value2-1")
		}
	} else {
		t.Error("Error GetAssociations, did not find", "superkey")
	}
}

// reverseKeys returns the set of keys reachable via a reverse lookup on value2.
func reverseKeys(t *testing.T, col *Collection, value2 string) map[string]bool {
	t.Helper()
	keys := make(map[string]bool)
	rev, found := col.GetReverseAssociations(value2)
	if !found {
		return keys
	}
	set, _ := col.GetTripleSet(rev, "", SortOptions{Limit: -1})
	set.ForEachKey(func(k string) { keys[k] = true })
	return keys
}

// TestReverseRelationAllowlist verifies that only whitelisted relations get a
// reverse index while forward lookups still work for every relation.
func TestReverseRelationAllowlist(t *testing.T) {
	db := NewDB(true)
	db.SetDefaultReverseRelations([]string{"tag"})
	col := db.GetCollection(CollectionKey{t.TempDir(), "revfilter"})

	col.Add("hashA", "tag", "sometag")
	col.Add("hashB", "tag", "sometag")
	col.Add("hashA", "filename", "myfile.ext")
	col.Sync()

	// Reverse lookup on the whitelisted relation's value works.
	if got := reverseKeys(t, col, "sometag"); !got["hashA"] || !got["hashB"] {
		t.Errorf("reverse lookup on tag 'sometag' = %v, want hashA and hashB", got)
	}

	// Reverse lookup on a non-whitelisted relation's value yields nothing.
	if got := reverseKeys(t, col, "myfile.ext"); len(got) != 0 {
		t.Errorf("reverse lookup on filename 'myfile.ext' = %v, want empty", got)
	}

	// Forward lookup still works for the non-whitelisted relation.
	fwd, found := col.GetAssociations("hashA")
	if !found {
		t.Fatal("forward lookup on hashA failed")
	}
	out, _ := col.GetTripleSet(fwd, "filename", SortOptions{Limit: -1})
	if !out["hashA"]["filename"].Has("myfile.ext") {
		t.Errorf("forward filename lookup missing value, got %v", out)
	}
}

// TestReverseAllowlistSurvivesReload verifies the two-pass loader rebuilds the
// same reverse index after a close/reopen cycle.
func TestReverseAllowlistSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "revreload")

	db := NewDB(true)
	db.SetDefaultReverseRelations([]string{"tag"})
	col := db.GetCollection(CollectionKey{dbPath, "col"})
	col.Add("hashA", "tag", "sometag")
	col.Add("hashA", "filename", "myfile.ext")
	col.Sync()
	col.closeDB()

	db = NewDB(true)
	db.SetDefaultReverseRelations([]string{"tag"})
	col = db.GetCollection(CollectionKey{dbPath, "col"})

	if got := reverseKeys(t, col, "sometag"); !got["hashA"] {
		t.Errorf("after reload, reverse lookup on 'sometag' = %v, want hashA", got)
	}
	if got := reverseKeys(t, col, "myfile.ext"); len(got) != 0 {
		t.Errorf("after reload, reverse lookup on 'myfile.ext' = %v, want empty", got)
	}
}

// getVal2s returns the set of value2s for (key, value1) via a forward Get.
func getVal2s(t *testing.T, col *Collection, key, value1 string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	res, found := col.Get(key, value1)
	if !found {
		return out
	}
	if v1, ok := res[key]; ok {
		if set, ok := v1[value1]; ok {
			set.ForEach(func(v2 string) { out[v2] = true })
		}
	}
	return out
}

// TestMemoryModeAddSyncGet is the core memory-mode regression: Add -> Sync (must
// not deadlock) -> Get returns values with no disk involved.
func TestMemoryModeAddSyncGet(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "c"})

	col.Add("hashA", "tag", "sometag")
	col.Add("hashA", "filename", "myfile.ext")
	col.Sync() // must return; previously deadlocked in memory mode

	if got := getVal2s(t, col, "hashA", "tag"); !got["sometag"] {
		t.Errorf("tag lookup = %v, want sometag", got)
	}
	if got := getVal2s(t, col, "hashA", "filename"); !got["myfile.ext"] {
		t.Errorf("filename lookup = %v, want myfile.ext", got)
	}
}

// TestDiskModeGetUsesCache verifies disk-mode queries serve triples from the
// cache: a second identical query does not add cache entries (all hits).
func TestDiskModeGetUsesCache(t *testing.T) {
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{t.TempDir(), "c"})
	col.Add("hashA", "tag", "sometag")
	col.Sync()

	if got := getVal2s(t, col, "hashA", "tag"); !got["sometag"] {
		t.Fatalf("first get = %v, want sometag", got)
	}
	sizeAfterFirst := col.cache.ll.Len()
	if sizeAfterFirst == 0 {
		t.Fatal("cache empty after first query; expected the triple to be cached")
	}
	// Second identical query must be served entirely from cache (no growth).
	getVal2s(t, col, "hashA", "tag")
	if got := col.cache.ll.Len(); got != sizeAfterFirst {
		t.Errorf("cache size changed on repeat query: %d -> %d (expected all hits)", sizeAfterFirst, got)
	}
}

// TestCacheInvalidationOnSlotReuse guards the correctness of freespace reuse:
// after Delete + a new Add that reuses the freed slot, a query must return the
// new triple, never the evicted one.
func TestCacheInvalidationOnSlotReuse(t *testing.T) {
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{t.TempDir(), "c"})

	col.Add("hashA", "tag", "old")
	col.Sync()
	getVal2s(t, col, "hashA", "tag") // populate cache for the triple's position

	if _, ok := col.Delete("hashA", "tag", "old"); !ok {
		t.Fatal("delete failed")
	}
	col.Add("hashA", "tag", "new")
	col.Sync()

	got := getVal2s(t, col, "hashA", "tag")
	if got["old"] {
		t.Error("stale value 'old' returned after delete + slot-reusing add")
	}
	if !got["new"] {
		t.Errorf("new value missing, got %v", got)
	}
}

// TestSortedPagination checks that GetPage returns triples in a stable order,
// honors Offset/Limit, and reports the pre-pagination total.
func TestSortedPagination(t *testing.T) {
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{t.TempDir(), "c"})

	const n = 25
	for i := 0; i < n; i++ {
		// zero-padded so lexical order is well-defined
		col.Add("key", "rel", "v"+padded(i))
	}
	col.Sync()

	tree, found := col.GetAssociations("key")
	if !found {
		t.Fatal("GetAssociations(key) not found")
	}

	// Full ordered result.
	_, all, total := col.GetPage(tree, "", SortOptions{Limit: -1})
	if total != n {
		t.Errorf("total = %d, want %d", total, n)
	}
	if len(all) != n {
		t.Fatalf("len(all) = %d, want %d", len(all), n)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Value2 > all[i].Value2 {
			t.Fatalf("results not sorted at %d: %q > %q", i, all[i-1].Value2, all[i].Value2)
		}
	}

	// A page in the middle.
	_, page, total := col.GetPage(tree, "", SortOptions{Offset: 10, Limit: 5})
	if total != n {
		t.Errorf("paged total = %d, want %d", total, n)
	}
	if len(page) != 5 {
		t.Fatalf("page len = %d, want 5", len(page))
	}
	for i := range page {
		if page[i].Value2 != all[10+i].Value2 {
			t.Errorf("page[%d] = %q, want %q", i, page[i].Value2, all[10+i].Value2)
		}
	}
}

// TestTornWriteRecovery appends a partial (sub-record) tail to a DB file and
// confirms reopening loads prior data instead of panicking.
func TestTornWriteRecovery(t *testing.T) {
	dir := t.TempDir()

	db := NewDB(true)
	col := db.GetCollection(CollectionKey{dir, "c"})
	col.Add("hashA", "tag", "sometag")
	col.Sync()
	col.closeDB()

	// Append a partial record (fewer than ENTRY_SIZE bytes).
	path := filepath.Join(dir, "c.tie")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Reopen: must not panic, and prior data must survive.
	db = NewDB(true)
	col = db.GetCollection(CollectionKey{dir, "c"})
	if got := getVal2s(t, col, "hashA", "tag"); !got["sometag"] {
		t.Errorf("after torn-write recovery, tag lookup = %v, want sometag", got)
	}
}

func padded(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}
