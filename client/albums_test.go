package client

import (
	"sort"
	"strings"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// audio builds a scanned audio file with the given tags.
func audio(path, artist, albumArtist, album string, year int) scannedFile {
	return scannedFile{
		path: path,
		size: 100,
		meta: metadata.Media{Artist: artist, AlbumArtist: albumArtist, Album: album, Year: year},
	}
}

// byDest sorts groups by Dest for deterministic assertions (merge order
// inside the grouper is map-iteration dependent).
func byDest(groups []AlbumGroup) {
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Dest != groups[j].Dest {
			return groups[i].Dest < groups[j].Dest
		}
		return groups[i].SourceDir < groups[j].SourceDir
	})
}

// findGroup returns the group whose Dest ends with suffix, or nil.
func findGroup(groups []AlbumGroup, suffix string) *AlbumGroup {
	for i := range groups {
		if strings.HasSuffix(groups[i].Dest, suffix) {
			return &groups[i]
		}
	}
	return nil
}

func TestGroupAutoPerfectLibrary(t *testing.T) {
	files := []scannedFile{
		audio("/lib/A-Ha/1985 - Hunting High and Low/01.flac", "A-Ha", "", "Hunting High and Low", 1985),
		audio("/lib/A-Ha/1985 - Hunting High and Low/02.flac", "A-Ha", "", "Hunting High and Low", 1985),
		audio("/lib/A-Ha/1988 - Stay on These Roads/01.flac", "A-Ha", "", "Stay on These Roads", 1988),
		audio("/lib/Adele/2008 - 19/01.flac", "Adele", "", "19", 2008),
	}
	groups := groupAlbums(files, GroupAuto)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3: %v", len(groups), groups)
	}
	for _, g := range groups {
		if !g.WholeTree() {
			t.Errorf("%s: consistent album dirs must stay whole-tree groups", g.SourceDir)
		}
	}
}

func TestGroupAutoMergesMultiDisc(t *testing.T) {
	files := []scannedFile{
		audio("/lib/Weird Al/1994 - Al in the Box/CD1/01.flac", "Weird Al", "", "Al in the Box", 1994),
		audio("/lib/Weird Al/1994 - Al in the Box/CD2/01.flac", "Weird Al", "", "Al in the Box", 1994),
		audio("/lib/Weird Al/1994 - Al in the Box/CD3/01.flac", "Weird Al", "", "Al in the Box", 1994),
	}
	groups := groupAlbums(files, GroupAuto)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 merged: %v", len(groups), groups)
	}
	g := groups[0]
	if g.WholeTree() {
		t.Fatal("merged multi-disc group must be a file-list group")
	}
	if len(g.Files) != 3 {
		t.Errorf("merged files = %d, want 3", len(g.Files))
	}
	if g.SourceDir != "/lib/Weird Al/1994 - Al in the Box" {
		t.Errorf("SourceDir = %q", g.SourceDir)
	}
}

func TestGroupAutoSplitsConflictingDir(t *testing.T) {
	files := []scannedFile{
		audio("/lib/mix/a.flac", "P", "", "Album X", 2000),
		audio("/lib/mix/b.flac", "Q", "", "Album Y", 2001),
		audio("/lib/mix/c.flac", "", "", "", 0),
	}
	groups := groupAlbums(files, GroupAuto)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3 (two splits + remainder): %v", len(groups), groups)
	}
	var remainder *AlbumGroup
	tracks := 0
	for i := range groups {
		tracks += groups[i].Tracks
		if groups[i].WholeTree() {
			t.Errorf("%s: a split directory must not keep a whole-tree group", groups[i].SourceDir)
		}
		if len(groups[i].metas) == 1 && groups[i].metas[0].Album == "" {
			remainder = &groups[i]
		}
	}
	if tracks != 3 {
		t.Errorf("total tracks across groups = %d, want 3 (no file lost or duplicated)", tracks)
	}
	if remainder == nil {
		t.Fatal("untagged file must form a remainder group")
	}
	if remainder.Dest != "/lib/mix" {
		t.Errorf("remainder Dest = %q, want /lib/mix", remainder.Dest)
	}
	if len(remainder.Warnings) == 0 {
		t.Error("remainder group must warn about untagged tracks")
	}
}

func TestGroupAutoMergesScatteredAlbum(t *testing.T) {
	files := []scannedFile{
		audio("/lib/d1/01.flac", "P", "", "Album X", 2000),
		audio("/lib/d2/02.flac", "P", "", "Album X", 2000),
		audio("/lib/d3/01.flac", "R", "", "Album Z", 2001),
	}
	groups := groupAlbums(files, GroupAuto)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2: %v", len(groups), groups)
	}
	var merged *AlbumGroup
	for i := range groups {
		if groups[i].Tracks == 2 {
			merged = &groups[i]
		}
	}
	if merged == nil {
		t.Fatal("scattered album X must merge into one group")
	}
	if merged.WholeTree() {
		t.Error("merged cross-dir group must be a file-list group")
	}
}

func TestGroupAutoVariousArtistsByAlbumArtist(t *testing.T) {
	files := []scannedFile{
		audio("/lib/va/01.flac", "Artist One", "Various Artists", "Comp", 2010),
		audio("/lib/va/02.flac", "Artist Two", "Various Artists", "Comp", 2010),
		audio("/lib/other/01.flac", "Artist Three", "Various Artists", "Comp", 2010),
	}
	groups := groupAlbums(files, GroupAuto)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 (album-artist unifies): %v", len(groups), groups)
	}
	g := groups[0]
	finalizeAlbumGroup(&g, "/music/{albumartist}/{year}. {album}")
	if g.Artist != "Various Artists" {
		t.Errorf("Artist = %q, want Various Artists", g.Artist)
	}
	if g.Dest != "/music/Various Artists/2010. Comp" {
		t.Errorf("Dest = %q", g.Dest)
	}
}

func TestGroupDirIsLiteral(t *testing.T) {
	files := []scannedFile{
		audio("/lib/A/Alb/01.flac", "A", "", "Alb", 2000),
		audio("/lib/A/Alb/CD2/01.flac", "A", "", "Alb", 2000),
	}
	groups := groupAlbums(files, GroupDir)
	if len(groups) != 2 {
		t.Fatalf("dir mode groups = %d, want 2 (no merging)", len(groups))
	}
	for _, g := range groups {
		if !g.WholeTree() {
			t.Errorf("%s: dir mode groups must be whole-tree", g.SourceDir)
		}
	}
}

func TestGroupTagsClustersAndBucketsUntagged(t *testing.T) {
	files := []scannedFile{
		audio("/lib/d1/01.flac", "P", "", "Album X", 2000),
		audio("/lib/d2/02.flac", "P", "", "Album X", 2000),
		audio("/lib/d1/raw.flac", "", "", "", 0),
	}
	groups := groupAlbums(files, GroupTags)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2: %v", len(groups), groups)
	}
	var cluster, rest *AlbumGroup
	for i := range groups {
		if groups[i].Tracks == 2 {
			cluster = &groups[i]
		} else {
			rest = &groups[i]
		}
	}
	if cluster == nil || rest == nil {
		t.Fatalf("want one 2-track cluster + one untagged group: %v", groups)
	}
	if rest.Dest != "/lib/d1" || len(rest.Warnings) == 0 {
		t.Errorf("untagged group = %+v, want dest /lib/d1 with warning", rest)
	}
}

func TestArchiveFormsSingleFileGroup(t *testing.T) {
	files := []scannedFile{
		{path: "/lib/zips/Artist - Album.zip", size: 5000, isArchive: true},
		audio("/lib/A/Alb/01.flac", "A", "", "Alb", 2000),
	}
	groups := groupAlbums(files, GroupAuto)
	var zip *AlbumGroup
	for i := range groups {
		if groups[i].IsArchive {
			zip = &groups[i]
		}
	}
	if zip == nil {
		t.Fatal("archive must form its own group")
	}
	if len(zip.Files) != 1 || zip.Tracks != 1 {
		t.Errorf("archive group = %+v", zip)
	}
	finalizeAlbumGroup(zip, "/music/{albumartist}/{album}")
	if zip.Dest != "/lib/zips" {
		t.Errorf("archive Dest = %q, want its containing dir /lib/zips", zip.Dest)
	}
	if zip.Album != "Artist - Album" {
		t.Errorf("archive display album = %q", zip.Album)
	}
}

func TestFinalizeAlbumGroupFallbacksAndWarnings(t *testing.T) {
	// Untagged dir: template can't render (empty album), falls back to source
	// path and warns.
	g := dirAlbumGroup("/lib/raw", []scannedFile{audio("/lib/raw/01.flac", "", "", "", 0)})
	finalizeAlbumGroup(&g, "/music/{albumartist}/{year}. {album}")
	if g.Dest != "/lib/raw" {
		t.Errorf("Dest = %q, want source-path fallback", g.Dest)
	}
	joined := strings.Join(g.Warnings, "; ")
	if !strings.Contains(joined, "no album tag") || !strings.Contains(joined, "no artist tag") {
		t.Errorf("warnings = %q", joined)
	}

	// {albumartist} falls back to artist when no album-artist tag exists.
	g = dirAlbumGroup("/lib/x", []scannedFile{audio("/lib/x/01.flac", "Miles Davis", "", "Kind of Blue", 1959)})
	finalizeAlbumGroup(&g, "/music/{albumartist}/{year}. {album}")
	if g.Dest != "/music/Miles Davis/1959. Kind of Blue" {
		t.Errorf("Dest = %q", g.Dest)
	}

	// Mixed artists without album-artist warn.
	g = dirAlbumGroup("/lib/va", []scannedFile{
		audio("/lib/va/01.flac", "A", "", "Comp", 2000),
		audio("/lib/va/02.flac", "B", "", "Comp", 2000),
	})
	finalizeAlbumGroup(&g, "")
	if !strings.Contains(strings.Join(g.Warnings, "; "), "mixed artists") {
		t.Errorf("warnings = %v", g.Warnings)
	}

	// A tag cluster that can't render the template falls back to
	// <common-dir>/<album>, not the bare common dir (which would merge
	// every template-less cluster of a loose library into one root).
	g = fileListGroup([]scannedFile{
		audio("/music/01.flac", "A", "", "No Year Album", 0),
		audio("/music/sub/02.flac", "A", "", "No Year Album", 0),
	})
	finalizeAlbumGroup(&g, "/m/{albumartist}/{year}. {album}")
	if g.Dest != "/music/No Year Album" {
		t.Errorf("Dest = %q, want /music/No Year Album", g.Dest)
	}
}

func TestWarnDestCollisions(t *testing.T) {
	groups := []AlbumGroup{
		{SourceDir: "/lib/a", Dest: "/music/Greatest Hits"},
		{SourceDir: "/lib/b", Dest: "/music/Greatest Hits"},
		{SourceDir: "/lib/c", Dest: "/music/Other"},
		// Archives import as single files: sharing a container dir is normal
		// and must not count as a collision.
		{SourceDir: "/lib", Dest: "/lib", IsArchive: true, Files: []string{"/lib/x.zip"}},
		{SourceDir: "/lib", Dest: "/lib", IsArchive: true, Files: []string{"/lib/y.zip"}},
	}
	warnDestCollisions(groups)
	if len(groups[0].Warnings) == 0 || len(groups[1].Warnings) == 0 {
		t.Error("colliding destinations must be flagged")
	}
	if len(groups[2].Warnings) != 0 {
		t.Error("unique destination must not be flagged")
	}
	if len(groups[3].Warnings) != 0 || len(groups[4].Warnings) != 0 {
		t.Error("archives sharing a container must not be flagged")
	}
}

func TestWarnNestedGroups(t *testing.T) {
	tree := dirAlbumGroup("/lib/A/Alb", []scannedFile{audio("/lib/A/Alb/01.flac", "A", "", "Alb", 2000)})
	nested := dirAlbumGroup("/lib/A/Alb/bonus", []scannedFile{audio("/lib/A/Alb/bonus/01.flac", "B", "", "Other", 2001)})
	sibling := dirAlbumGroup("/lib/A/Other", []scannedFile{audio("/lib/A/Other/01.flac", "C", "", "Third", 2002)})
	groups := []AlbumGroup{tree, nested, sibling}
	warnNestedGroups(groups)
	if len(groups[1].Warnings) == 0 {
		t.Error("nested group must be flagged")
	}
	if len(groups[2].Warnings) != 0 {
		t.Error("sibling must not be flagged")
	}
}

func TestCommonDir(t *testing.T) {
	for _, tc := range []struct {
		paths []string
		want  string
	}{
		{[]string{"/a/b/c/1", "/a/b/d/2"}, "/a/b"},
		{[]string{"/a/b/1"}, "/a/b"},
		{[]string{"/a/x/1", "/b/y/2"}, "/"},
		{[]string{"/a/b/1", "/a/b/2"}, "/a/b"},
	} {
		if got := commonDir(tc.paths); got != tc.want {
			t.Errorf("commonDir(%v) = %q, want %q", tc.paths, got, tc.want)
		}
	}
}

func TestRenderDestTemplateAlbumArtist(t *testing.T) {
	m := metadata.Media{Artist: "Track Artist", AlbumArtist: "Album Artist", Album: "X"}
	got, err := renderDestTemplate("/m/{albumartist}", m)
	if err != nil || got != "/m/Album Artist" {
		t.Errorf("render = %q, %v", got, err)
	}
	// Falls back to the track artist when no album artist is tagged.
	m.AlbumArtist = ""
	got, err = renderDestTemplate("/m/{albumartist}", m)
	if err != nil || got != "/m/Track Artist" {
		t.Errorf("fallback render = %q, %v", got, err)
	}
	// Both empty: error, so the caller falls back to the source path.
	m.Artist = ""
	if _, err = renderDestTemplate("/m/{albumartist}", m); err == nil {
		t.Error("empty albumartist must error (fallback to source path)")
	}
}

func TestParseGroupMode(t *testing.T) {
	for s, want := range map[string]GroupMode{"": GroupAuto, "auto": GroupAuto, "dir": GroupDir, "tags": GroupTags} {
		if got, err := ParseGroupMode(s); err != nil || got != want {
			t.Errorf("ParseGroupMode(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	if _, err := ParseGroupMode("bogus"); err == nil {
		t.Error("bogus mode must error")
	}
}

func TestFinalizePlanSortsByDest(t *testing.T) {
	groups := []AlbumGroup{
		dirAlbumGroup("/lib/zeta", []scannedFile{audio("/lib/zeta/01.flac", "Zeta", "", "B", 2000)}),
		dirAlbumGroup("/lib/alpha", []scannedFile{audio("/lib/alpha/01.flac", "Alpha", "", "A", 2000)}),
	}
	finalizePlan(groups, "/m/{albumartist}/{album}")
	if groups[0].Dest != "/m/Alpha/A" || groups[1].Dest != "/m/Zeta/B" {
		t.Fatalf("plan not sorted by dest: %q, %q", groups[0].Dest, groups[1].Dest)
	}
	_ = byDest
	_ = findGroup
}
