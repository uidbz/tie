package tiedb

// Benchmark for the HasRelation scan added in #124. It is a full pass over the
// forward index — there is no index keyed by relation alone — so the point of
// these numbers is what that pass costs at a realistic size, next to the
// index hit a term query gets. The fixture is shaped like a media collection:
// one subject per file/dir node, ~10 triples each (tie-type, path/filename/
// filesize/media-type, tag-date, a few tags), and 2 % of the subjects carrying
// an extra relation the query looks for.

import (
	"fmt"
	"testing"
)

func relationFixture(b *testing.B, subjects int) *Collection {
	b.Helper()
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{b.TempDir(), "bench"})
	for i := 0; i < subjects; i++ {
		key := fmt.Sprintf("hash-%08d", i)
		if i%50 == 0 {
			col.Add(key, "tie-type", "directory")
			col.Add(key, "path", "tie:/a/b/"+key)
		} else {
			col.Add(key, "tie-type", "file")
			col.Add(key, "filename", key+".jpg")
			col.Add(key, "filesize", "1024")
			col.Add(key, "media-type", "image/jpeg")
			col.Add(key, "parent", fmt.Sprintf("hash-%08d", i/50))
		}
		col.Add(key, "tag-date", "2026-01-02 03:04:05.000000")
		col.Add(key, "tag", "seen")
		if i%50 == 0 {
			col.Add(key, "marker", "yes") // the relation the scan looks for
		}
	}
	col.Sync()
	return col
}

func BenchmarkQueryHasRelation(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		col := relationFixture(b, n)
		q := TagQuery{HasRelation: "marker", Filter: "marker", Sort: SortOptions{Limit: -1}}
		b.Run(fmt.Sprintf("subjects=%d", n), func(b *testing.B) {
			b.ReportMetric(float64(n), "subjects/op")
			for b.Loop() {
				sorted, _, _ := col.QueryTags(q)
				if len(sorted) == 0 {
					b.Fatal("the scan matched nothing")
				}
			}
		})
	}
}

// BenchmarkQueryTermIsIndexHit is the baseline: the same algebra seeded from the
// reverse index instead of a scan.
func BenchmarkQueryTermIsIndexHit(b *testing.B) {
	col := relationFixture(b, 100_000)
	q := TagQuery{Include: []string{"directory"}, Filter: "tie-type", Reverse: true, Sort: SortOptions{Limit: -1}}
	b.ReportMetric(float64(100_000), "subjects/op")
	for b.Loop() {
		sorted, _, _ := col.QueryTags(q)
		if len(sorted) == 0 {
			b.Fatal("the term query matched nothing")
		}
	}
}
