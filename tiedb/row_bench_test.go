package tiedb

import (
	"strconv"
	"testing"
)

// makeSortedTriples builds a []StringTriple shaped like a real query page:
// keyCount keys, each with relCount relations, each holding valsPerRel values.
// The slice is already in the sorted order RowsFromSorted expects.
func makeSortedTriples(keyCount, relCount, valsPerRel int) []StringTriple {
	out := make([]StringTriple, 0, keyCount*relCount*valsPerRel)
	for k := 0; k < keyCount; k++ {
		key := "key" + strconv.Itoa(k)
		for r := 0; r < relCount; r++ {
			rel := "rel" + strconv.Itoa(r)
			for v := 0; v < valsPerRel; v++ {
				out = append(out, StringTriple{Key: key, Value1: rel, Value2: "val" + strconv.Itoa(v)})
			}
		}
	}
	return out
}

// legacyTripleSet is the nested-map shape GetPage used to build for every
// lookup. The type is gone from the package (§8 S9), but the benchmark baseline
// it provides is worth keeping: it is the measurement that justifies the flat
// fold. Memory is weighted >= load speed per CLAUDE.md, so both runs report
// allocs.
type legacyTripleSet map[string]map[string]map[string]struct{}

func buildTripleSet(sorted []StringTriple) legacyTripleSet {
	result := make(legacyTripleSet)
	for _, t := range sorted {
		if result[t.Key] == nil {
			result[t.Key] = make(map[string]map[string]struct{})
		}
		if result[t.Key][t.Value1] == nil {
			result[t.Key][t.Value1] = make(map[string]struct{})
		}
		result[t.Key][t.Value1][t.Value2] = struct{}{}
	}
	return result
}

func BenchmarkRowsFromSorted(b *testing.B) {
	sorted := makeSortedTriples(1000, 8, 3)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := RowsFromSorted(sorted)
		if len(rows) == 0 {
			b.Fatal("no rows")
		}
	}
}

// BenchmarkTripleSetBuild is the old nested-map baseline; compare its ns/op and
// B/op against BenchmarkRowsFromSorted to confirm the flat fold does not regress.
func BenchmarkTripleSetBuild(b *testing.B) {
	sorted := makeSortedTriples(1000, 8, 3)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set := buildTripleSet(sorted)
		if len(set) == 0 {
			b.Fatal("no keys")
		}
	}
}

// BenchmarkExpandKeys measures the multi-key batch fetch on a populated
// collection: this is the FUSE-listing / batch-attribute path.
func BenchmarkExpandKeys(b *testing.B) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "expandbench"})
	const keyCount = 500
	keys := make([]string, keyCount)
	for k := 0; k < keyCount; k++ {
		key := "key" + strconv.Itoa(k)
		keys[k] = key
		col.Add(key, "tag", "sometag")
		col.Add(key, "filename", "file"+strconv.Itoa(k)+".ext")
		col.Add(key, "filesize", strconv.Itoa(k*1024))
	}
	col.Sync()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := col.ExpandKeys(keys, "")
		if len(rows) != keyCount {
			b.Fatalf("got %d rows, want %d", len(rows), keyCount)
		}
	}
}
