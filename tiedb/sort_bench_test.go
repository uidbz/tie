package tiedb

import (
	"fmt"
	"strconv"
	"testing"
)

// benchTree builds a collection of subjects each holding n triples, and returns
// it plus the subject keys.
func benchTree(b *testing.B, subjects, perSubject int) (*Collection, []string) {
	b.Helper()
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "bench"})
	keys := make([]string, subjects)
	for i := range keys {
		keys[i] = "subject-" + strconv.Itoa(i)
		for j := 0; j < perSubject; j++ {
			col.Add(keys[i], "rel-"+strconv.Itoa(j), "value-"+strconv.Itoa(i)+"-"+strconv.Itoa(j))
		}
	}
	return col, keys
}

// BenchmarkSortSingleKey measures one Sort over a single-triple association set
// — the per-key cost ExpandKeys, Get and firstValueOf pay.
func BenchmarkSortSingleKey(b *testing.B) {
	col, keys := benchTree(b, 1000, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree, _ := col.GetAssociations(keys[i%len(keys)])
		col.Sort(tree, "", SortOptions{Limit: -1})
	}
}

// BenchmarkDump is tie dump: one ForEachTriple over the whole collection.
func BenchmarkDump(b *testing.B) {
	col, _ := benchTree(b, 20000, 3)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		col.ForEachTriple(func(StringTriple) { n++ })
	}
}

// BenchmarkGetValueLong measures resolving a long (multi-level) value: the old
// getValue recursion re-appended at every level (O(L^2) bytes copied).
func BenchmarkGetValueLong(b *testing.B) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "longvalue"})
	// 20 * 24 bytes = 20 trie levels deep.
	long := ""
	for i := 0; i < 20; i++ {
		long += "0123456789abcdef01234567"
	}
	col.Add(long, "rel", "v")
	id, level, ok := col.getEntryFromString(long)
	if !ok {
		b.Fatal("value not interned")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v := col.getValue(level, id); len(v) == 0 {
			b.Fatal("empty value")
		}
	}
}

// BenchmarkGetFilteredFanIn measures Get(hub, rel) on a node holding many
// relations — the shape of a directory with thousands of tags, or a hub file
// with thousands of edges. The filter should skip the relations that cannot
// match before resolveEntry reads their record (§5.2 item 5).
func BenchmarkGetFilteredFanIn(b *testing.B) {
	db := NewDB(true)
	defer db.Close()
	col := db.GetCollection(CollectionKey{b.TempDir(), "fanin"})

	const relations = 2000
	for i := 0; i < relations; i++ {
		if err := col.Add("hub", fmt.Sprintf("rel-%04d", i), fmt.Sprintf("val-%04d", i)); err != nil {
			b.Fatal(err)
		}
	}
	col.Sync()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set, found := col.Get("hub", "rel-0007")
		if !found || len(set["hub"]["rel-0007"]) != 1 {
			b.Fatalf("Get(hub, rel-0007) = %v, %v", set, found)
		}
	}
}
