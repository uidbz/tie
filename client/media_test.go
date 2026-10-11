package client

import (
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestSplitGenres(t *testing.T) {
	cases := map[string][]string{
		"":                      nil,
		"Jazz":                  {"Jazz"},
		"Rock; Pop":             {"Rock", "Pop"},
		"Rock/Pop,  Rock ":      {"Rock", "Pop"},
		"Electronic\x00Ambient": {"Electronic", "Ambient"},
		"rock;Rock":             {"rock"},
	}
	for in, want := range cases {
		if got := SplitGenres(in); !slices.Equal(got, want) {
			t.Errorf("SplitGenres(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscNumberFromName(t *testing.T) {
	cases := map[string]int{"cd1": 1, "CD 2": 2, "Disc 3": 3, "disk-4": 4, "disc_10": 10, "bonus": 0, "cd": 0, "Disc 2 extras": 0}
	for in, want := range cases {
		if got := discNumberFromName(in); got != want {
			t.Errorf("discNumberFromName(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseMediaTime(t *testing.T) {
	want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if got, ok := ParseMediaTime(FormatMediaTime(want)); !ok || !got.Equal(want) {
		t.Fatalf("round trip: %v %v", got, ok)
	}
	local := want.In(time.Local)
	if got, ok := ParseMediaTime(local.Format(tagDateFormat)); !ok || !got.Equal(want) {
		t.Fatalf("tag-date layout: %v %v", got, ok)
	}
	if _, ok := ParseMediaTime("nope"); ok {
		t.Fatal("garbage parsed")
	}
	// Fixed width: string order is chronological.
	a, b := FormatMediaTime(want), FormatMediaTime(want.Add(time.Hour*24*400))
	if !(a < b) || len(a) != len(b) {
		t.Fatalf("not fixed-width sortable: %s %s", a, b)
	}
}

func TestClassifyPlay(t *testing.T) {
	cases := []struct {
		listened, dur float64
		want          PlayOutcome
		ok            bool
	}{
		{100, 200, OutcomeComplete, true},  // half
		{240, 1200, OutcomeComplete, true}, // 4 min of a long track
		{10, 200, OutcomeSkip, true},
		{40, 200, OutcomeSkip, true},    // < 25 %
		{60, 200, "", false},            // in between
		{20, 25, OutcomeSkip, true},     // too short to ever count
		{300, 0, OutcomeComplete, true}, // unknown length, long listen
		{20, 0, "", false},
	}
	for _, c := range cases {
		got, ok := ClassifyPlay(c.listened, c.dur)
		if got != c.want || ok != c.ok {
			t.Errorf("ClassifyPlay(%v,%v) = %q,%v want %q,%v", c.listened, c.dur, got, ok, c.want, c.ok)
		}
	}
}

func TestPlayEventIDDeterministic(t *testing.T) {
	a := PlayEventID("pwplay@host", 7)
	if a != PlayEventID("pwplay@host", 7) || a == PlayEventID("pwplay@host", 8) || a == PlayEventID("other", 7) {
		t.Fatal("PlayEventID not a function of (source, seq)")
	}
	if len(a) != 64 {
		t.Fatalf("len %d", len(a))
	}
}

func TestPlayStatsApply(t *testing.T) {
	var s PlayStats
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(48 * time.Hour)
	s.apply(PlayEvent{PlayedAt: t2, Outcome: OutcomeComplete})
	s.apply(PlayEvent{PlayedAt: t1, Outcome: OutcomeComplete})
	s.apply(PlayEvent{PlayedAt: t2, Outcome: OutcomeSkip})
	if s.PlayCount != 2 || s.SkipCount != 1 || !s.FirstPlayed.Equal(t1) || !s.LastPlayed.Equal(t2) || !s.LastSkipped.Equal(t2) {
		t.Fatalf("%+v", s)
	}
	back := StatsFromRow(Row{Attributes: s.relations()})
	if back != s {
		t.Fatalf("relations round trip: %+v vs %+v", back, s)
	}
}

func TestPlaylistTracksOrder(t *testing.T) {
	r := Row{Attributes: map[string][]string{RelPlaylistTrack: {PlaylistEntry(10, "c"), PlaylistEntry(2, "b"), PlaylistEntry(0, "a"), PlaylistEntry(3, "a")}}}
	if got := PlaylistTracks(r); !slices.Equal(got, []string{"a", "b", "a", "c"}) {
		t.Fatalf("%v", got)
	}
}

// --- integration (test-env) ---

func uniqueTestPath(t *testing.T) string {
	return fmt.Sprintf("tie:/media-test/%s-%d", t.Name(), time.Now().UnixNano())
}

// addFakeTrack writes the triples an audio import would, without a blob.
func addFakeTrack(t *testing.T, tc *TieClient, parent DirUID, n int, album string, disc int, extra map[string]string) string {
	t.Helper()
	hash := newSubjectUID()
	b := tc.NewBatch()
	b.Add(hash, "tie-type", "audio-file")
	b.Add(hash, "tie-type", "file")
	b.Add(hash, "media-type", "audio/flac")
	b.Add(hash, "filename", fmt.Sprintf("%02d.flac", n))
	b.Add(hash, "title", fmt.Sprintf("Track %d", n))
	b.Add(hash, "artist", "Some Artist")
	b.Add(hash, "album", album)
	b.Add(hash, "year", "1999")
	b.Add(hash, "track", strconv.Itoa(n))
	b.Add(hash, "duration", "100.000")
	b.Add(hash, "genre", "Jazz")
	b.Set(hash, "tag-date", []string{time.Now().Format(tagDateFormat)})
	if disc > 0 {
		b.Add(hash, "disc", strconv.Itoa(disc))
	}
	for k, v := range extra {
		b.Add(hash, k, v)
	}
	b.Add(hash, "parent", string(parent))
	if _, err := tc.Batch(b); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestIndexCollectionMultiDisc(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	requireServer(t, tc)
	root := uniqueTestPath(t)
	albumUID, err := tc.MkTieDirAll(root)
	if err != nil {
		t.Fatal(err)
	}
	cd1, _ := tc.MkTieDirAll(root + "/cd1")
	cd2, _ := tc.MkTieDirAll(root + "/cd2")
	art, _ := tc.MkTieDirAll(root + "/artwork")
	if err := tc.SetDirType(albumUID, "audio-dir"); err != nil {
		t.Fatal(err)
	}
	// The "nested" album under the root must keep its own members.
	nested, _ := tc.MkTieDirAll(root + "/nested")
	_ = tc.SetDirType(nested, "audio-dir")

	var tracks []string
	tracks = append(tracks, addFakeTrack(t, tc, cd1, 1, "Double", 0, nil))
	tracks = append(tracks, addFakeTrack(t, tc, cd1, 2, "Double", 0, nil))
	tracks = append(tracks, addFakeTrack(t, tc, cd2, 1, "Double", 2, nil))
	cover := newSubjectUID()
	b := tc.NewBatch()
	b.Add(cover, "tie-type", "image-file")
	b.Add(cover, "filename", "front.jpg")
	b.Add(cover, "parent", string(art))
	if _, err := tc.Batch(b); err != nil {
		t.Fatal(err)
	}
	nestedTrack := addFakeTrack(t, tc, nested, 1, "Other", 0, nil)

	res, err := tc.IndexCollection(albumUID, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Members != 4 || res.AddedMembers != 4 || res.Discs != 2 {
		t.Fatalf("result %+v", res)
	}
	again, err := tc.IndexCollection(albumUID, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Ops != 0 {
		t.Fatalf("second index not idempotent: %+v", again)
	}

	rows, err := tc.CollectionMembers(string(albumUID))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Key] = true
	}
	for _, h := range append(tracks, cover) {
		if !got[h] {
			t.Errorf("member %s missing from member-of listing (reverse index configured?)", h)
		}
	}
	if got[nestedTrack] {
		t.Error("nested album's track counted as member")
	}

	row, err := tc.Get(string(albumUID))
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		RelTrackCount: "3", RelDiscCount: "2", RelDuration: "300.000", RelGenre: "Jazz",
		RelAlbumArtist: "Some Artist", "album": "Double", "year": "1999", RelMemberCount: "4",
	} {
		if got := RowFirst(row, rel); got != want {
			t.Errorf("album %s = %q, want %q", rel, got, want)
		}
	}
	if RowFirst(row, RelAddedAt) == "" {
		t.Error("no added-at on album")
	}
	tr, _ := tc.Get(tracks[0])
	if RowFirst(tr, RelDisc) != "1" {
		t.Errorf("cd1 track disc = %q, want 1 (from folder)", RowFirst(tr, RelDisc))
	}
	d2, _ := tc.Get(string(cd2))
	if !RowHas(d2, "tie-type", TypeDisc) || RowFirst(d2, RelDisc) != "2" {
		t.Errorf("cd2 not labeled as disc 2: %v", d2.Attributes)
	}

	// Moving a track out drops its stale edge.
	if _, err := tc.Delete(tracks[2], "parent", string(cd2)); err != nil {
		t.Fatal(err)
	}
	r3, err := tc.IndexCollection(albumUID, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r3.DroppedStale != 1 {
		t.Fatalf("stale not dropped: %+v", r3)
	}
}

func TestRecordPlaysIdempotentAndStats(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	requireServer(t, tc)
	track, album := newSubjectUID(), newSubjectUID()
	source := "test@" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t0 := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	events := []PlayEvent{
		{Source: source, Seq: 1, Subject: track, Collection: album, PlayedAt: t0, Listened: 120, Duration: 200, Outcome: OutcomeComplete},
		{Source: source, Seq: 2, Subject: track, Collection: album, PlayedAt: t0.Add(10 * time.Minute), Listened: 5, Duration: 200, Outcome: OutcomeSkip},
		{Source: source, Seq: 3, Subject: track, Collection: album, PlayedAt: t0.Add(20 * time.Minute), Listened: 200, Duration: 200, Outcome: OutcomeComplete},
	}
	n, err := tc.RecordPlays(events)
	if err != nil || n != 3 {
		t.Fatalf("record: %d %v", n, err)
	}
	n, err = tc.RecordPlays(events[:2])
	if err != nil || n != 0 {
		t.Fatalf("re-record not idempotent: %d %v", n, err)
	}
	for _, key := range []string{track, album} {
		row, err := tc.Get(key)
		if err != nil {
			t.Fatal(err)
		}
		s := StatsFromRow(row)
		if s.PlayCount != 2 || s.SkipCount != 1 || !s.FirstPlayed.Equal(t0) || !s.LastPlayed.Equal(t0.Add(20*time.Minute)) {
			t.Fatalf("%s stats %+v", key, s)
		}
	}
	recent, err := tc.RecentPlays(PlayQuery{Since: t0.Add(-time.Second), Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var mine []PlayEvent
	for _, e := range recent {
		if e.Source == source {
			mine = append(mine, e)
		}
	}
	if len(mine) != 3 || !mine[0].PlayedAt.After(mine[1].PlayedAt) || mine[0].Listened != 200 {
		t.Fatalf("recent plays %+v", mine)
	}

	// Corrupt the counter, then rebuild it from the events.
	if err := tc.Set(track, RelPlayCount, []string{"99"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.RecomputeStats(); err != nil {
		t.Fatal(err)
	}
	row, _ := tc.Get(track)
	if got := StatsFromRow(row).PlayCount; got != 2 {
		t.Fatalf("restat play-count %d", got)
	}
}

func TestPlaylistLifecycleAndMigration(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	requireServer(t, tc)
	album, err := tc.MkTieDirAll(uniqueTestPath(t))
	if err != nil {
		t.Fatal(err)
	}
	a := addFakeTrack(t, tc, album, 1, "A", 0, nil)
	b := addFakeTrack(t, tc, album, 2, "A", 0, nil)
	c := addFakeTrack(t, tc, album, 3, "A", 0, nil)

	uid, err := tc.CreatePlaylist("Mix", []string{b, a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tc.ReadPlaylist(uid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Mix" || !slices.Equal(p.Tracks, []string{b, a, b}) || p.Legacy {
		t.Fatalf("%+v", p)
	}
	if err := tc.SetPlaylistTracks(uid, []string{c, a}); err != nil {
		t.Fatal(err)
	}
	ra, _ := tc.Get(a)
	rb, _ := tc.Get(b)
	rc, _ := tc.Get(c)
	if !RowHas(ra, RelMemberOf, uid) || RowHas(rb, RelMemberOf, uid) || !RowHas(rc, RelMemberOf, uid) {
		t.Fatal("member-of not diffed")
	}

	// A legacy playlist: audio-dir + fake parent edges.
	legacy := newSubjectUID()
	bt := tc.NewBatch()
	bt.Add(legacy, "tie-type", "audio-dir")
	bt.Add(legacy, "tie-type", "directory")
	bt.Add(legacy, "name", "Old")
	bt.Add(legacy, "tag", PlaylistTag)
	bt.Set(legacy, "tag-date", []string{time.Now().Format(time.DateTime)})
	bt.Add(legacy, RelPlaylistTrack, PlaylistEntry(0, c))
	bt.Add(legacy, RelPlaylistTrack, PlaylistEntry(1, a))
	bt.Add(c, "parent", legacy)
	bt.Add(a, "parent", legacy)
	if _, err := tc.Batch(bt); err != nil {
		t.Fatal(err)
	}
	n, err := tc.MigrateLegacyPlaylists()
	if err != nil || n < 1 {
		t.Fatalf("migrate %d %v", n, err)
	}
	rl, _ := tc.Get(legacy)
	if !RowHas(rl, "tie-type", TypePlaylist) || RowHas(rl, "tie-type", "audio-dir") || RowHas(rl, "tie-type", "directory") {
		t.Fatalf("legacy types %v", rl.Attributes["tie-type"])
	}
	ra, _ = tc.Get(a)
	if RowHas(ra, "parent", legacy) || !RowHas(ra, RelMemberOf, legacy) || !RowHas(ra, "parent", string(album)) {
		t.Fatalf("legacy edges not migrated: %v", ra.Attributes)
	}
	if err := tc.DeletePlaylist(uid); err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Get(uid); err == nil {
		t.Fatal("playlist still exists")
	}
	ra, _ = tc.Get(a)
	if RowHas(ra, RelMemberOf, uid) {
		t.Fatal("member-of survived delete")
	}
}
