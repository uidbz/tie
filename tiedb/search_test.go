package tiedb

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func searchFixture(t *testing.T) *Collection {
	t.Helper()
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "search"})

	// Two Beatles albums and one unrelated album, with enough fields to
	// exercise cross-field matching.
	col.Add("album-abbey", "tie-type", "audio-dir")
	col.Add("album-abbey", "album", "Abbey Road")
	col.Add("album-abbey", "album-artist", "The Beatles")
	col.Add("album-abbey", "genre", "rock")

	col.Add("album-letitbe", "tie-type", "audio-dir")
	col.Add("album-letitbe", "album", "Let It Be")
	col.Add("album-letitbe", "album-artist", "The Beatles")

	col.Add("album-nevermind", "tie-type", "audio-dir")
	col.Add("album-nevermind", "album", "Nevermind")
	col.Add("album-nevermind", "album-artist", "Nirvana")

	col.Add("track-come", "tie-type", "audio-file")
	col.Add("track-come", "title", "Come Together")
	col.Add("track-come", "artist", "The Beatles")
	col.Add("track-come", "album", "Abbey Road")
	col.Add("track-come", "filename", "01 - Come Together.flac")
	col.Add("track-come", "member-of", "album-abbey")

	col.Add("track-something", "tie-type", "audio-file")
	col.Add("track-something", "title", "Something")
	col.Add("track-something", "artist", "The Beatles")
	col.Add("track-something", "album", "Abbey Road")
	col.Add("track-something", "filename", "02 - Something.flac")
	col.Add("track-something", "member-of", "album-abbey")

	col.Add("track-smells", "tie-type", "audio-file")
	col.Add("track-smells", "title", "Smells Like Teen Spirit")
	col.Add("track-smells", "artist", "Nirvana")
	col.Add("track-smells", "album", "Nevermind")
	col.Add("track-smells", "filename", "01 - Smells Like Teen Spirit.flac")

	// Accented metadata for folding.
	col.Add("album-beyonce", "tie-type", "audio-dir")
	col.Add("album-beyonce", "album", "Beyoncé")
	col.Add("album-beyonce", "album-artist", "Beyoncé")

	// A directory/file pair for tie-fm-style searches.
	col.Add("dir-music", "tie-type", "directory")
	col.Add("dir-music", "name", "Music")
	col.Add("dir-music", "path", "tie:/Music")
	col.Add("file-notes", "tie-type", "file")
	col.Add("file-notes", "filename", "notes.txt")
	col.Add("file-notes", "path", "tie:/Music/notes.txt")

	return col
}

func searchKeys(matches []SearchMatch) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Key)
	}
	return out
}

func TestSearchCrossFieldWords(t *testing.T) {
	col := searchFixture(t)

	// "come abbey": "come" comes from the title, "abbey" from the album, so
	// neither field alone contains both tokens.
	matches, total, _ := col.Search(SearchQuery{
		Text:      "come abbey",
		Relations: []string{"title", "album"},
		Mode:      SearchWords,
		Limit:     10,
	})
	if total != 1 {
		t.Fatalf("total = %d, want 1 (%v)", total, searchKeys(matches))
	}
	if matches[0].Key != "track-come" {
		t.Errorf("best match = %q, want track-come", matches[0].Key)
	}
	if matches[0].Relation != "album" && matches[0].Relation != "title" {
		t.Errorf("representative relation = %q, want album or title", matches[0].Relation)
	}
}

func TestSearchSubstringPrefixFuzzy(t *testing.T) {
	col := searchFixture(t)

	sub, _, _ := col.Search(SearchQuery{
		Text:      "abbey ro",
		Relations: []string{"album"},
		TieTypes:  []string{"audio-dir"},
		Mode:      SearchSubstring,
		Limit:     10,
	})
	if len(sub) != 1 || sub[0].Key != "album-abbey" {
		t.Errorf("substring matches = %v, want album-abbey", searchKeys(sub))
	}

	pre, _, _ := col.Search(SearchQuery{
		Text:      "nev",
		Relations: []string{"album"},
		TieTypes:  []string{"audio-dir"},
		Mode:      SearchPrefix,
		Limit:     10,
	})
	if len(pre) != 1 || pre[0].Key != "album-nevermind" {
		t.Errorf("prefix matches = %v, want album-nevermind", searchKeys(pre))
	}

	// "beetles" is one edit from "beatles".
	fuzzy, _, _ := col.Search(SearchQuery{
		Text:      "beetles",
		Relations: []string{"artist", "album-artist"},
		Mode:      SearchFuzzy,
		Limit:     10,
	})
	if len(fuzzy) == 0 {
		t.Fatal("fuzzy search found nothing for beetles")
	}
	for _, m := range fuzzy {
		if m.Key == "album-nevermind" || m.Key == "track-smells" {
			t.Errorf("fuzzy search returned unrelated key %q", m.Key)
		}
	}
}

func TestSearchTieTypeNarrowing(t *testing.T) {
	col := searchFixture(t)

	matches, total, _ := col.Search(SearchQuery{
		Text:      "beatles",
		Relations: []string{"artist", "album-artist"},
		TieTypes:  []string{"audio-dir"},
		Limit:     10,
	})
	if total != 2 {
		t.Fatalf("audio-dir total = %d, want 2 (%v)", total, searchKeys(matches))
	}
	for _, m := range matches {
		if !strings.HasPrefix(m.Key, "album-") {
			t.Errorf("type-narrowed search returned non-directory %q", m.Key)
		}
	}

	// A type that no subject carries matches nothing.
	matches, total, _ = col.Search(SearchQuery{
		Text:      "beatles",
		Relations: []string{"artist"},
		TieTypes:  []string{"video-file"},
	})
	if total != 0 || len(matches) != 0 {
		t.Errorf("video-file search = %v, want empty", searchKeys(matches))
	}
}

// TestSearchTypeFilterWithoutReverseIndex exercises the full-scan fallback: the
// server may omit tie-type from its reverse relations, and search must then
// filter by direct forward-set membership instead.
func TestSearchTypeFilterWithoutReverseIndex(t *testing.T) {
	db := NewDB(false)
	db.SetDefaultReverseRelations([]string{"parent"}) // deliberately no tie-type
	col := db.GetCollection(CollectionKey{"mem", "no-reverse"})
	col.Add("album-1", "tie-type", "audio-dir")
	col.Add("album-1", "album", "Abbey Road")
	col.Add("file-1", "tie-type", "audio-file")
	col.Add("file-1", "album", "Abbey Road")

	matches, total, _ := col.Search(SearchQuery{
		Text:      "abbey",
		Relations: []string{"album"},
		TieTypes:  []string{"audio-dir"},
	})
	if total != 1 || len(matches) != 1 || matches[0].Key != "album-1" {
		t.Errorf("fallback type filter = %v (total %d), want album-1", searchKeys(matches), total)
	}
}

func TestSearchRelationFilterAndUnknownRelation(t *testing.T) {
	col := searchFixture(t)

	matches, total, _ := col.Search(SearchQuery{
		Text:      "abbey",
		Relations: []string{"filename"},
	})
	if total != 0 || len(matches) != 0 {
		t.Errorf("filename-only search = %v, want empty", searchKeys(matches))
	}

	matches, total, _ = col.Search(SearchQuery{
		Text:      "abbey",
		Relations: []string{"no-such-relation"},
	})
	if total != 0 || len(matches) != 0 {
		t.Errorf("unknown relation search = %v, want empty", searchKeys(matches))
	}
}

func TestSearchUnicodeFolding(t *testing.T) {
	col := searchFixture(t)

	matches, total, _ := col.Search(SearchQuery{
		Text:      "beyonce",
		Relations: []string{"album", "album-artist"},
	})
	if total == 0 {
		t.Fatal("accent-folded search found nothing")
	}
	if matches[0].Key != "album-beyonce" {
		t.Errorf("best match = %q, want album-beyonce", matches[0].Key)
	}
}

func TestSearchPaging(t *testing.T) {
	col := searchFixture(t)

	all, total, _ := col.Search(SearchQuery{
		Text:      "beatles",
		Relations: []string{"artist", "album-artist"},
		Limit:     100,
	})
	if total != 4 {
		t.Fatalf("total = %d, want 4 (%v)", total, searchKeys(all))
	}
	page, pageTotal, _ := col.Search(SearchQuery{
		Text:      "beatles",
		Relations: []string{"artist", "album-artist"},
		Offset:    1,
		Limit:     1,
	})
	if pageTotal != total || len(page) != 1 {
		t.Fatalf("page = %v (total %d), want one result and total %d", searchKeys(page), pageTotal, total)
	}
	if page[0].Key != all[1].Key {
		t.Errorf("page[0] = %q, want %q", page[0].Key, all[1].Key)
	}
}

func TestSearchInvalidation(t *testing.T) {
	col := searchFixture(t)

	if _, total, _ := col.Search(SearchQuery{Text: "karma", Relations: []string{"title"}}); total != 0 {
		t.Fatal("unexpected match before add")
	}
	col.Add("track-karma", "tie-type", "audio-file")
	col.Add("track-karma", "title", "Karma Police")
	if _, total, _ := col.Search(SearchQuery{Text: "karma", Relations: []string{"title"}}); total != 1 {
		t.Fatalf("after add total = %d, want 1", total)
	}
	col.Delete("track-karma", "title", "Karma Police")
	if _, total, _ := col.Search(SearchQuery{Text: "karma", Relations: []string{"title"}}); total != 0 {
		t.Fatalf("after delete total = %d, want 0", total)
	}
}

func TestSearchCacheDisabled(t *testing.T) {
	col := searchFixture(t)
	col.searchMaxBytes = 0 // force a rebuild for every request

	for i := 0; i < 3; i++ {
		matches, total, _ := col.Search(SearchQuery{Text: "abbey", Relations: []string{"album"}})
		if total != 3 || len(matches) != 3 {
			t.Fatalf("run %d: total = %d, matches = %v", i, total, searchKeys(matches))
		}
	}
	if col.searchCache != nil && len(col.searchCache) != 0 {
		t.Errorf("cache disabled but %d corpora installed", len(col.searchCache))
	}
}

func TestSearchEmptyAndLimits(t *testing.T) {
	col := searchFixture(t)

	for _, q := range []SearchQuery{
		{Text: "", Relations: []string{"album"}},
		{Text: "   ", Relations: []string{"album"}},
		{Text: "abbey", Relations: nil},
		{Text: "!!!", Relations: []string{"album"}},
	} {
		matches, total, _ := col.Search(q)
		if total != 0 || len(matches) != 0 {
			t.Errorf("query %+v = %v, want empty", q, searchKeys(matches))
		}
	}

	// Limit is clamped rather than trusted.
	_, _, rev := col.Search(SearchQuery{Text: "the", Relations: []string{"artist"}, Limit: 1 << 30})
	if rev == 0 {
		// Revision may legitimately be 0 only before the first index mutation;
		// the fixture has plenty, so a zero here means the request was dropped.
		t.Error("search returned revision 0")
	}
}

func TestSearchSharedValuesAndHashSubjects(t *testing.T) {
	db := NewDB(false)
	db.SetBlobPolicy(HexHashBlobPolicy(func(s string) bool {
		return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
	}))
	col := db.GetCollection(CollectionKey{"mem", "hash-search"})
	hash := strings.Repeat("ab", 32)
	col.Add(hash, "tie-type", "audio-file")
	col.Add(hash, "title", "Hash Song")
	col.Add(hash, "artist", "Hash Artist")

	matches, total, _ := col.Search(SearchQuery{Text: "hash song", Relations: []string{"title"}, TieTypes: []string{"audio-file"}})
	if total != 1 || len(matches) != 1 || matches[0].Key != hash {
		t.Fatalf("hash subject search = %v (total %d), want %s", searchKeys(matches), total, hash)
	}

	// One shared value object for a repeated artist.
	col.Add("other", "tie-type", "audio-file")
	col.Add("other", "artist", "Hash Artist")
	q := SearchQuery{Relations: []string{"artist"}}
	c := col.corpusFor(corpusKey(q), q)
	var shared *searchValue
	for _, s := range c.subjects {
		for _, f := range s.fields {
			if f.value.text != "Hash Artist" {
				continue
			}
			if shared == nil {
				shared = f.value
				continue
			}
			if f.value != shared {
				t.Error("repeated value was not shared between subjects")
			}
		}
	}
	if shared == nil {
		t.Fatal("artist value missing from corpus")
	}
}

func TestSearchConcurrentWrites(t *testing.T) {
	col := searchFixture(t)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			col.Add(fmt.Sprintf("new-%d", i), "title", fmt.Sprintf("New Song %d", i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			col.Search(SearchQuery{Text: "new song", Relations: []string{"title"}, Limit: 10})
		}
	}()
	wg.Wait()

	_, total, _ := col.Search(SearchQuery{Text: "new song", Relations: []string{"title"}, Limit: 10})
	if total != 200 {
		t.Errorf("after concurrent writes total = %d, want 200", total)
	}
}
