package tiedb

import (
	"fmt"
	"testing"
)

// searchBenchFixture builds n track-like subjects with realistic field sizes.
func searchBenchFixture(b *testing.B, n int) *Collection {
	b.Helper()
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"bench", "search"})
	artists := []string{"The Beatles", "Nirvana", "Beyoncé", "Pink Floyd", "Radiohead"}
	albums := []string{"Abbey Road", "Nevermind", "Lemonade", "The Dark Side of the Moon", "OK Computer"}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("track-%d", i)
		col.Add(key, "tie-type", "audio-file")
		col.Add(key, "artist", artists[i%len(artists)])
		col.Add(key, "album", albums[i%len(albums)])
		col.Add(key, "title", fmt.Sprintf("Song Number %d", i))
		col.Add(key, "filename", fmt.Sprintf("%02d - Song Number %d.flac", i%20+1, i))
	}
	return col
}

func BenchmarkSearchBuildAndQuery(b *testing.B) {
	for _, n := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("subjects=%d", n), func(b *testing.B) {
			col := searchBenchFixture(b, n)
			q := SearchQuery{
				Text:      "beatles abbey",
				Relations: []string{"artist", "album", "title", "filename"},
				TieTypes:  []string{"audio-file"},
				Limit:     100,
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				col.searchGen.Add(1) // force a cold corpus each iteration
				matches, _, _ := col.Search(q)
				if len(matches) == 0 {
					b.Fatal("no matches")
				}
			}
		})
	}
}

func BenchmarkSearchWarm(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		b.Run(fmt.Sprintf("subjects=%d", n), func(b *testing.B) {
			col := searchBenchFixture(b, n)
			q := SearchQuery{
				Text:      "song number",
				Relations: []string{"artist", "album", "title", "filename"},
				TieTypes:  []string{"audio-file"},
				Limit:     100,
			}
			// Prime the corpus.
			col.Search(q)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				matches, total, _ := col.Search(q)
				if total == 0 || len(matches) == 0 {
					b.Fatal("no matches")
				}
			}
		})
	}
}

func BenchmarkSearchFuzzyWarm(b *testing.B) {
	col := searchBenchFixture(b, 50000)
	q := SearchQuery{
		Text:      "beetles",
		Relations: []string{"artist", "album", "title"},
		TieTypes:  []string{"audio-file"},
		Mode:      SearchFuzzy,
		Limit:     100,
	}
	col.Search(q)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		col.Search(q)
	}
}
