package client

import (
	"path/filepath"
	"testing"

	"github.com/uidbz/tie/internal/testenv"
)

// searchSeed adds a small library with unique keys so the shared test
// collection stays independent of other tests.
func searchSeed(t *testing.T, tie *TieClient) {
	t.Helper()
	b := tie.NewBatch()
	add := func(key string, triples ...[2]string) {
		for _, tr := range triples {
			b.Add(key, tr[0], tr[1])
		}
	}
	dir := "searchtest-album-abbey"
	add(dir,
		[2]string{string(RelType), TieAudioDir.String()},
		[2]string{string(RelAlbum), "Abbey Road"},
		[2]string{string(RelAlbumArtist), "The Beatles"},
	)
	track := "searchtest-track-come"
	add(track,
		[2]string{string(RelType), TieAudioFile.String()},
		[2]string{string(RelTitle), "Come Together"},
		[2]string{string(RelArtist), "The Beatles"},
		[2]string{string(RelAlbum), "Abbey Road"},
		[2]string{string(RelFilename), "01 - Come Together.flac"},
		[2]string{string(RelMemberOf), dir},
	)
	other := "searchtest-track-smells"
	add(other,
		[2]string{string(RelType), TieAudioFile.String()},
		[2]string{string(RelTitle), "Smells Like Teen Spirit"},
		[2]string{string(RelArtist), "Nirvana"},
		[2]string{string(RelAlbum), "Nevermind"},
	)
	file := "searchtest-file-notes"
	add(file,
		[2]string{string(RelType), TieFile.String()},
		[2]string{string(RelFilename), "searchtest-notes.txt"},
		[2]string{string(RelPath), "tie:/searchtest/notes.txt"},
	)
	if _, err := tie.Batch(b); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
}

func TestSearchIntegration(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)
	searchSeed(t, tie)

	// Cross-field words search, scoped to audio subjects, with grouping data.
	matches, total, err := tie.Search(SearchSpec{
		Query:     "beatles abbey",
		Relations: []Relation{RelAlbum, RelAlbumArtist, RelArtist, RelTitle},
		TieTypes:  []TieType{TieAudioFile, TieAudioDir},
		Mode:      SearchWords,
		Limit:     10,
		Expand:    true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total == 0 || len(matches) == 0 {
		t.Fatal("Search found nothing")
	}
	var foundDir bool
	for _, m := range matches {
		if m.Key == "searchtest-album-abbey" {
			foundDir = true
		}
		if m.Attributes == nil {
			t.Errorf("%s: Expand requested but Attributes is nil", m.Key)
		}
	}
	if !foundDir {
		t.Errorf("matches = %v, want the Abbey Road album", matches)
	}

	// Profile scoping keeps a text file out of an audio search.
	matches, total, err = tie.Search(AudioSearchProfile.Spec("notes"))
	if err != nil {
		t.Fatalf("profile search: %v", err)
	}
	for _, m := range matches {
		if m.Key == "searchtest-file-notes" {
			t.Errorf("audio profile returned a plain file: %v", m)
		}
	}
	_ = total

	// No matches is a normal empty result, not ErrNotFound.
	matches, total, err = tie.Search(SearchSpec{
		Query:     "zzzz-no-such-search",
		Relations: []Relation{RelAlbum},
	})
	if err != nil {
		t.Fatalf("empty search returned an error: %v", err)
	}
	if len(matches) != 0 || total != 0 {
		t.Errorf("empty search = %v (total %d)", matches, total)
	}

	// A write invalidates the corpus: the new value is searchable immediately.
	if _, err := tie.Add("searchtest-track-karma", string(RelTitle), "Karma Police"); err != nil {
		t.Fatal(err)
	}
	matches, _, err = tie.Search(SearchSpec{
		Query:     "karma police",
		Relations: []Relation{RelTitle},
	})
	if err != nil {
		t.Fatalf("search after add: %v", err)
	}
	if len(matches) != 1 || matches[0].Key != "searchtest-track-karma" {
		t.Errorf("after add matches = %v, want searchtest-track-karma", matches)
	}
}

func TestSearchPageRevision(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)
	searchSeed(t, tie)

	first, err := tie.SearchPage(SearchSpec{
		Query:     "beatles",
		Relations: []Relation{RelAlbum, RelArtist},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if first.Revision == 0 {
		t.Fatal("Revision = 0 after seeded writes")
	}
	if first.Total == 0 || len(first.Matches) == 0 {
		t.Fatal("SearchPage found nothing")
	}

	key := "searchtest-track-revision"
	if _, err := tie.Add(key, string(RelTitle), "Revision Test"); err != nil {
		t.Fatal(err)
	}
	second, err := tie.SearchPage(SearchSpec{
		Query:     "revision test",
		Relations: []Relation{RelTitle},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchPage after write: %v", err)
	}
	if second.Revision <= first.Revision {
		t.Errorf("revision did not advance: first %d, second %d", first.Revision, second.Revision)
	}
	if second.Total != 1 || len(second.Matches) != 1 || second.Matches[0].Key != key {
		t.Errorf("after-write page = %v (total %d), want %s", second.Matches, second.Total, key)
	}
}

func TestSearchEmptyRelationsAndUnknownServer(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	matches, total, err := tie.Search(SearchSpec{Query: "anything"})
	if err != nil {
		t.Fatalf("empty relation search: %v", err)
	}
	if len(matches) != 0 || total != 0 {
		t.Errorf("empty relation search = %v (total %d), want empty", matches, total)
	}
}

func TestSearchLocalReadOnly(t *testing.T) {
	env := testenv.StartT(t)
	cfg := Config{
		TripleStoreURL: env.TripleStoreURL,
		Namespace:      "Collections",
		Collection:     "Main",
	}
	remote := NewTieClientFor(cfg, "")
	if _, err := remote.Add("local-search-album", string(RelType), TieAudioDir.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Add("local-search-album", string(RelAlbum), "Abbey Road"); err != nil {
		t.Fatal(err)
	}
	if err := remote.Sync(); err != nil {
		t.Fatal(err)
	}
	env.Stop()

	file := filepath.Join(env.DBDir, "Collections", "Main.tie")
	localCfg, err := ConfigForFile(file)
	if err != nil {
		t.Fatalf("ConfigForFile: %v", err)
	}
	localCfg.ReadOnly = true
	local := NewTieClientFor(localCfg, "")
	defer local.Close()

	matches, total, err := local.Search(SearchSpec{
		Query:     "abbey",
		Relations: []Relation{RelAlbum},
		TieTypes:  []TieType{TieAudioDir},
	})
	if err != nil {
		t.Fatalf("local read-only search: %v", err)
	}
	if total != 1 || len(matches) != 1 || matches[0].Key != "local-search-album" {
		t.Errorf("local search = %v (total %d), want local-search-album", matches, total)
	}
}

func TestSearchProfiles(t *testing.T) {
	if len(AudioSearchProfile.Relations) == 0 || len(AudioSearchProfile.TieTypes) == 0 {
		t.Fatal("AudioSearchProfile is empty")
	}
	if len(FileSearchProfile.Relations) == 0 || len(FileSearchProfile.TieTypes) == 0 {
		t.Fatal("FileSearchProfile is empty")
	}
	spec := FileSearchProfile.Spec("notes")
	if spec.Query != "notes" || spec.Mode != SearchWords {
		t.Errorf("file profile spec = %+v", spec)
	}
	var hasDir, hasFile bool
	for _, tt := range FileSearchProfile.TieTypes {
		switch tt {
		case TieDirectory:
			hasDir = true
		case TieFile:
			hasFile = true
		}
	}
	if !hasDir || !hasFile {
		t.Errorf("FileSearchProfile types = %v, want directory and file", FileSearchProfile.TieTypes)
	}
}

func TestSearchTieTypeNames(t *testing.T) {
	got := tieTypeStrings([]TieType{TieAudioFile}, []string{" live-album ", ""})
	if len(got) != 2 || got[0] != TieAudioFile.String() || got[1] != "live-album" {
		t.Errorf("tieTypeStrings = %v, want [audio-file live-album]", got)
	}
	profile := SearchProfile{TieTypeNames: []string{"live-album"}}
	if spec := profile.Spec("x"); len(spec.TieTypeNames) != 1 || spec.TieTypeNames[0] != "live-album" {
		t.Errorf("profile spec TieTypeNames = %v", spec.TieTypeNames)
	}
	if !IsBuiltinTieType("audio-file") || IsBuiltinTieType("live-album") {
		t.Error("IsBuiltinTieType classification is wrong")
	}
}
