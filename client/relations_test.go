package client

import "testing"

// TestRelationNamesMatchTheStoredStrings pins every relation the client writes
// to the exact string stored in existing databases. These names used to come
// from a stringer-generated int enum; the typed constants have to render the
// same bytes, or a rename silently orphans every triple that uses the old one
// while the store keeps answering queries for it (§7.10).
func TestRelationNamesMatchTheStoredStrings(t *testing.T) {
	for _, tc := range []struct {
		rel  Relation
		want string
	}{
		{RelUID, "tie-uid"},
		{RelFilename, "filename"},
		{RelFilesize, "filesize"},
		{RelName, "name"},
		{RelMediaType, "media-type"},
		{RelFileHost, "filehost"},
		{RelTag, "tag"},
		{RelPath, "path"},
		{RelParent, "parent"},
		{RelTags, "tags"},
		{RelTagDate, "tag-date"},
		{RelCollection, "collection"},
		{RelType, "tie-type"},
		{RelAll, "all"},
		{RelTitle, "title"},
		{RelArtist, "artist"},
		{RelAlbum, "album"},
		{RelYear, "year"},
		{RelTrack, "track"},
		{RelTiedirHash, "tiedir-hash"},
		{RelDirUID, "dir-uid"},
		{RelVersionOf, "version-of"},
		{RelVersionDate, "version-date"},
		{RelFavorite, "favorite"},
		{RelMemberOf, "member-of"},
		{RelAddedAt, "added-at"},
		{RelMemberCount, "member-count"},
		{RelThumbnail, "thumbnail"},
		{RelRating, "rating"},
		{RelAlbumArtist, "album-artist"},
		{RelGenre, "genre"},
		{RelComposer, "composer"},
		{RelDisc, "disc"},
		{RelDiscTotal, "disc-total"},
		{RelTrackTotal, "track-total"},
		{RelDuration, "duration"},
		{RelTrackCount, "track-count"},
		{RelDiscCount, "disc-count"},
		{RelPlayCount, "play-count"},
		{RelSkipCount, "skip-count"},
		{RelFirstPlayed, "first-played"},
		{RelLastPlayed, "last-played"},
		{RelLastSkipped, "last-skipped"},
		{RelRuntime, "runtime"},
		{RelRole, "role"},
		{RelSeason, "season"},
		{RelEpisode, "episode"},
		{RelEpisodeTitle, "episode-title"},
		{RelAirDate, "air-date"},
		{RelResumePosition, "resume-position"},
		{RelTakenFrom, "taken-from"},
		{RelTakenTo, "taken-to"},
		{RelLocation, "location"},
		{RelTMDBID, "tmdb-id"},
		{RelIMDBID, "imdb-id"},
		{RelTVDBID, "tvdb-id"},
	} {
		if string(tc.rel) != tc.want {
			t.Errorf("relation constant is stored as %q, want %q", string(tc.rel), tc.want)
		}
	}
}

// TestRelationsAreUnique: two constants with the same string would mean two
// names for one fact, which is how the pre-#77 vocabulary drifted.
func TestRelationsAreUnique(t *testing.T) {
	seen := map[Relation]string{}
	for _, tc := range []struct {
		rel  Relation
		name string
	}{
		{RelUID, "tie-uid"},
		{RelFilename, "filename"},
		{RelFilesize, "filesize"},
		{RelName, "name"},
		{RelMediaType, "media-type"},
		{RelFileHost, "filehost"},
		{RelTag, "tag"},
		{RelPath, "path"},
		{RelParent, "parent"},
		{RelTags, "tags"},
		{RelTagDate, "tag-date"},
		{RelCollection, "collection"},
		{RelType, "tie-type"},
		{RelAll, "all"},
		{RelTitle, "title"},
		{RelArtist, "artist"},
		{RelAlbum, "album"},
		{RelYear, "year"},
		{RelTrack, "track"},
		{RelTiedirHash, "tiedir-hash"},
		{RelDirUID, "dir-uid"},
		{RelVersionOf, "version-of"},
		{RelVersionDate, "version-date"},
		{RelFavorite, "favorite"},
		{RelMemberOf, "member-of"},
		{RelAddedAt, "added-at"},
		{RelMemberCount, "member-count"},
		{RelThumbnail, "thumbnail"},
		{RelRating, "rating"},
		{RelAlbumArtist, "album-artist"},
		{RelGenre, "genre"},
		{RelComposer, "composer"},
		{RelDisc, "disc"},
		{RelDiscTotal, "disc-total"},
		{RelTrackTotal, "track-total"},
		{RelDuration, "duration"},
		{RelTrackCount, "track-count"},
		{RelDiscCount, "disc-count"},
		{RelPlayCount, "play-count"},
		{RelSkipCount, "skip-count"},
		{RelFirstPlayed, "first-played"},
		{RelLastPlayed, "last-played"},
		{RelLastSkipped, "last-skipped"},
		{RelRuntime, "runtime"},
		{RelRole, "role"},
		{RelSeason, "season"},
		{RelEpisode, "episode"},
		{RelEpisodeTitle, "episode-title"},
		{RelAirDate, "air-date"},
		{RelResumePosition, "resume-position"},
		{RelTakenFrom, "taken-from"},
		{RelTakenTo, "taken-to"},
		{RelLocation, "location"},
		{RelTMDBID, "tmdb-id"},
		{RelIMDBID, "imdb-id"},
		{RelTVDBID, "tvdb-id"},
	} {
		if other, dup := seen[tc.rel]; dup {
			t.Errorf("%s and %s are both stored as %q", other, tc.name, string(tc.rel))
		}
		seen[tc.rel] = tc.name
	}
}
