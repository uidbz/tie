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

// BenchmarkSortByValue orders a directory's children by one of their relations
// — what `tie ls --sort-by-value year` and the album views do. Ordering used to
// answer each key's value with a full Get (a Sort, a decode and nested maps) per
// key (§5.2 item 6).
func BenchmarkSortByValue(b *testing.B) {
	db := NewDB(true)
	defer db.Close()
	col := db.GetCollection(CollectionKey{b.TempDir(), "byvalue"})

	const keys = 2000
	for i := 0; i < keys; i++ {
		k := "key" + strconv.Itoa(i)
		for _, pair := range [][2]string{
			{"parent", "dir"},
			{"year", strconv.Itoa(1900 + i%100)},
			{"filename", k + ".flac"},
			{"tie-type", "audio-file"},
		} {
			if err := col.Add(k, pair[0], pair[1]); err != nil {
				b.Fatal(err)
			}
		}
	}
	col.Sync()

	dir, found := col.GetReverseAssociations("dir")
	if !found {
		b.Fatal("no reverse associations for dir")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, sorted, total := col.GetPage(dir, "", SortOptions{SortByValue: "year", Limit: -1})
		if len(sorted) != total || total != keys {
			b.Fatalf("sorted %d of %d, want %d", len(sorted), total, keys)
		}
	}
}

// BenchmarkCoTags is the faceted-refinement query the tag UI runs: given the
// current tag filter, which further tags exist on the matching set? It used to
// resolve the matched set twice — once to enumerate keys, once per key for the
// tag values — decoding every triple both times (§5.2 item 8).
func BenchmarkCoTags(b *testing.B) {
	db := NewDB(true)
	defer db.Close()
	col := db.GetCollection(CollectionKey{b.TempDir(), "cotags"})

	const files = 2000
	const pool = 40
	const perFile = 8
	for i := 0; i < files; i++ {
		f := "file" + strconv.Itoa(i)
		if err := col.Add(f, "tie-type", "audio-file"); err != nil {
			b.Fatal(err)
		}
		if err := col.Add(f, "parent", "dir"); err != nil {
			b.Fatal(err)
		}
		for t := 0; t < perFile; t++ {
			tag := "tag" + strconv.Itoa((i*perFile+t)%pool)
			if err := col.Add(f, "tag", tag); err != nil {
				b.Fatal(err)
			}
		}
	}
	col.Sync()

	q := TagQuery{Include: []string{"tag0"}, Reverse: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tags, ok := col.CoTags(q, "tag")
		if !ok || len(tags) == 0 {
			b.Fatalf("CoTags = %v, %v", tags, ok)
		}
	}
}

// BenchmarkQueryPaged is the second page of a tag query over a large match set
// — tie-fm walking a 100k-item tag directory. It used to resolve and sort every
// match for every page (§5.2 item 4).
func BenchmarkQueryPaged(b *testing.B) {
	db := NewDB(true)
	defer db.Close()
	col := db.GetCollection(CollectionKey{b.TempDir(), "paged"})

	const matches = 20000
	for i := 0; i < matches; i++ {
		key := "file" + strconv.Itoa(i)
		if err := col.Add(key, "tag", "shared"); err != nil {
			b.Fatal(err)
		}
		if err := col.Add(key, "tie-type", "audio-file"); err != nil {
			b.Fatal(err)
		}
	}
	col.Sync()

	set, found := col.GetReverseAssociations("shared")
	if !found {
		b.Fatal("no reverse associations for the shared tag")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, page, total := col.GetPage(set, "", SortOptions{Offset: 1000, Limit: 1000})
		if total != matches || len(page) != 1000 {
			b.Fatalf("page = %d rows of %d, want 1000 of %d", len(page), total, matches)
		}
	}
}
