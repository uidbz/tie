package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// planCoverSidecars runs the planner's finalize pass over hand-built groups and
// returns the cover sidecars each group picked up.
func planCoverSidecars(groups []AlbumGroup, tree []pathSize) [][]string {
	finalizePlan(groups, "", tree)
	out := make([][]string, len(groups))
	for i, g := range groups {
		for _, s := range g.Sidecars {
			if isImageName(s) {
				out[i] = append(out[i], s)
			}
		}
	}
	return out
}

func sidecarsBySource(groups []AlbumGroup, tree []pathSize) map[string][]string {
	sc := planCoverSidecars(groups, tree)
	m := map[string][]string{}
	for i, g := range groups {
		m[g.SourceDir] = sc[i]
	}
	return m
}

// An explicit file-list group must not adopt artwork from a sibling album
// directory, from a non-art subfolder, or from an art-named folder that holds
// audio of its own; ties between equally good images resolve by path, not by
// map iteration order.
func TestAddGroupCoverSidecarScope(t *testing.T) {
	t.Run("sibling album", func(t *testing.T) {
		g := fileListGroup([]scannedFile{audio("/m/Various/01.flac", "A", "", "Split", 2000)})
		other := fileListGroup([]scannedFile{audio("/m/Various/OtherAlbum/01.flac", "B", "", "Other", 2001)})
		other.Files = nil // a whole-tree dir group: it keeps its own art
		tree := []pathSize{
			{"/m/Various/01.flac", 100},
			{"/m/Various/OtherAlbum/01.flac", 100},
			{"/m/Various/OtherAlbum/cover.jpg", 50},
		}
		got := sidecarsBySource([]AlbumGroup{g, other}, tree)
		if s := got["/m/Various"]; len(s) != 0 {
			t.Errorf("split group adopted a sibling album's artwork: %v", s)
		}
	})

	t.Run("art folder with audio", func(t *testing.T) {
		g := fileListGroup([]scannedFile{audio("/m/Alb/01.flac", "A", "", "Alb", 2000)})
		bonus := fileListGroup([]scannedFile{audio("/m/Alb/Extras/bonus.flac", "A", "", "Bonus", 2000)})
		tree := []pathSize{
			{"/m/Alb/01.flac", 100},
			{"/m/Alb/Extras/bonus.flac", 100},
			{"/m/Alb/Extras/front.jpg", 50},
			{"/m/Alb/Artwork/Scans/front.jpg", 40},
		}
		got := sidecarsBySource([]AlbumGroup{g, bonus}, tree)
		if s := got["/m/Alb"]; len(s) != 1 || s[0] != "/m/Alb/Artwork/Scans/front.jpg" {
			t.Errorf("sidecars = %v, want only the Artwork/ image (Extras/ holds audio)", s)
		}
	})

	t.Run("deterministic tie", func(t *testing.T) {
		for run := 0; run < 50; run++ {
			g := fileListGroup([]scannedFile{
				audio("/m/Alb/CD1/01.flac", "A", "", "Alb", 2000),
				audio("/m/Alb/CD2/01.flac", "A", "", "Alb", 2000),
				audio("/m/Alb/CD3/01.flac", "A", "", "Alb", 2000),
			})
			tree := []pathSize{
				{"/m/Alb/CD1/01.flac", 100}, {"/m/Alb/CD1/folder.jpg", 10},
				{"/m/Alb/CD2/01.flac", 100}, {"/m/Alb/CD2/folder.jpg", 10},
				{"/m/Alb/CD3/01.flac", 100}, {"/m/Alb/CD3/folder.jpg", 10},
			}
			got := planCoverSidecars([]AlbumGroup{g}, tree)[0]
			if len(got) != 1 || got[0] != "/m/Alb/CD1/folder.jpg" {
				t.Fatalf("run %d: sidecars = %v, want /m/Alb/CD1/folder.jpg", run, got)
			}
		}
	})
}

// IndexCollection records only high/medium-confidence artwork, within the
// resolver's depth limit, and leaves a collection with a legacy thumbnail
// alone.
func TestIndexCollectionCoverRules(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	requireServer(t, tc)

	addImage := func(t *testing.T, parent DirUID, name string) string {
		t.Helper()
		h := newSubjectUID()
		b := tc.NewBatch()
		b.Add(h, "tie-type", "image-file")
		b.Add(h, "filename", name)
		b.Add(h, "parent", string(parent))
		if _, err := tc.Batch(b); err != nil {
			t.Fatal(err)
		}
		return h
	}
	mk := func(t *testing.T, p string) DirUID {
		t.Helper()
		uid, err := tc.MkTieDirAll(p)
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}
	index := func(t *testing.T, uid DirUID) (IndexResult, Row) {
		t.Helper()
		res, err := tc.IndexCollection(uid, IndexOptions{})
		if err != nil {
			t.Fatal(err)
		}
		row, err := tc.Get(string(uid))
		if err != nil {
			t.Fatal(err)
		}
		return res, row
	}

	t.Run("low confidence", func(t *testing.T) {
		root := uniqueTestPath(t)
		uid := mk(t, root)
		addFakeTrack(t, tc, uid, 1, "Low", 0, nil)
		addImage(t, uid, "back.jpg")
		res, row := index(t, uid)
		if res.Cover != "" || RowFirst(row, RelCover) != "" {
			t.Errorf("back.jpg recorded as cover: %q / %v", res.Cover, row.Attributes[string(RelCoverSource)])
		}
	})

	t.Run("depth limit", func(t *testing.T) {
		root := uniqueTestPath(t)
		uid := mk(t, root)
		deep := mk(t, root+"/a/b/c")
		addFakeTrack(t, tc, uid, 1, "Deep", 0, nil)
		addImage(t, deep, "front.jpg")
		res, row := index(t, uid)
		if res.Cover != "" || RowFirst(row, RelCover) != "" {
			t.Errorf("image three levels down recorded as cover: %v", row.Attributes[string(RelCoverSource)])
		}
	})

	t.Run("legacy thumbnail", func(t *testing.T) {
		root := uniqueTestPath(t)
		uid := mk(t, root)
		addFakeTrack(t, tc, uid, 1, "Thumb", 0, nil)
		addImage(t, uid, "front.jpg")
		if err := tc.Set(string(uid), string(RelThumbnail), []string{"legacy-thumb"}); err != nil {
			t.Fatal(err)
		}
		res, row := index(t, uid)
		if res.Cover != "" || RowFirst(row, RelCover) != "" {
			t.Errorf("cover recorded over a legacy thumbnail: %v", row.Attributes[string(RelCoverSource)])
		}
		if RowFirst(row, RelThumbnail) != "legacy-thumb" {
			t.Errorf("thumbnail changed: %v", row.Attributes[string(RelThumbnail)])
		}
	})

	t.Run("medium still recorded", func(t *testing.T) {
		root := uniqueTestPath(t)
		uid := mk(t, root)
		art := mk(t, root+"/Artwork")
		addFakeTrack(t, tc, uid, 1, "Ok", 0, nil)
		addImage(t, uid, "back.jpg")
		want := addImage(t, art, "Artist - Album.jpg")
		res, _ := index(t, uid)
		if res.Cover != want {
			t.Errorf("cover = %q, want the medium-confidence image %q", res.Cover, want)
		}
	})
}

// A filehost failure while probing embedded pictures is an error, not
// ErrNoCover: callers cache ErrNoCover. A file without a picture still means
// "no cover".
func TestResolveCoverEmbeddedErrors(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	requireServer(t, tc)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusForbidden)
	}))
	defer down.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not an audio container at all"))
	}))
	defer plain.Close()

	root := uniqueTestPath(t)
	uid, err := tc.MkTieDirAll(root)
	if err != nil {
		t.Fatal(err)
	}
	addFakeTrack(t, tc, uid, 1, "Embedded", 0, nil)

	_, err = ResolveCover(tc, string(uid), CoverOptions{
		Host: FileHost{URL: down.URL}, IncludeEmbedded: true, DryRun: true,
	})
	if err == nil || errors.Is(err, ErrNoCover) {
		t.Errorf("filehost outage reported as %v, want a fetch error", err)
	}

	_, err = ResolveCover(tc, string(uid), CoverOptions{
		Host: FileHost{URL: plain.URL}, IncludeEmbedded: true, DryRun: true,
	})
	if !errors.Is(err, ErrNoCover) {
		t.Errorf("track without a picture: err = %v, want ErrNoCover", err)
	}
}
