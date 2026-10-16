package tiedb

import (
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
