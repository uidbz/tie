package client

// media.go defines tie's shared media vocabulary: the relations and tie-type
// labels every client (CLI, tie-audio, tie-view, tie-fm) agrees on for media
// collections (albums, playlists, picture sets, series, movies), plus the
// collection contract that makes a collection a first-class subject rather
// than just "a directory that happens to hold media".
//
// # The collection contract
//
// A collection subject (an audio-dir/image-dir DirUID, an audio-archive blob,
// a playlist, …) carries
//
//	(C, tie-type,     <kind>)        e.g. audio-dir, playlist, tv-series
//	(C, name|album,   <display>)
//	(C, thumbnail,    <thumbHash>)   cover / poster (optional)
//	(C, added-at,     RFC3339)       first time the collection entered the store
//	(C, member-count, <n>)
//
// and every member points at it:
//
//	(member, member-of, C)
//
// member-of is distinct from the directory tree's parent edge: parent says
// where a file is *filed*, member-of what it *belongs to*. A track in
// <album>/cd2/ has parent = the cd2 node but member-of = the album; a track
// listed in three playlists has three extra member-of edges but still one
// parent. Listing a collection is therefore one reverse member-of query with
// Expand, whatever the folder depth (the triplestore indexes member-of in
// reverse by default since v0.6.0).
//
// added-at is written only when absent (EnsureAddedAt), so unlike tag-date —
// the *last* import time, rewritten by every re-import — it is a stable
// "date added" for "recently added" views.
//
// # Audio
//
// Track (content hash): title, artist, album-artist, album, year, track,
// track-total, disc, disc-total, genre (one triple per genre), composer,
// duration (seconds).
//
// Album (audio-dir DirUID or audio-archive hash): album, album-artist, artist,
// year, genre (top genres), track-count, disc-count, duration (total), plus the
// collection contract. Disc subdirectories of a multi-disc album carry
// (uid, tie-type, disc) and (uid, disc, <n>).
//
// Playlist: see playlist.go (tie-type playlist, ordered playlist-track
// entries, member-of edges from each track; no fake parent edges).
//
// Statistics (see history.go) are denormalized onto tracks and collections:
// play-count, skip-count, first-played, last-played, last-skipped.
// rating ("1".."5") is the shared star rating for any subject (images,
// tracks, albums); the "favorite" tag is the shared "liked" flag.
//
// # Video and pictures (vocabulary; adopted by clients incrementally)
//
// Movie: (C, tie-type, movie) on a directory (film + subtitles + extras) or a
// single video file; title, year, runtime (seconds), tmdb-id, imdb-id,
// thumbnail (poster). Members of a movie directory carry role
// subtitle|extra|trailer|feature. A franchise is a collection whose members
// are movies (member-of).
//
// TV: tv-series (title, year, tvdb-id, thumbnail) ⟵member-of⟵ tv-season
// (season <n>) ⟵member-of⟵ episode files, which also point at the series
// directly and carry season, episode, episode-title, air-date (YYYY-MM-DD).
// "Next episode" orders by (season, episode); watch progress is a play event
// with play-position plus resume-position on the episode.
//
// Picture set: image-dir plus taken-from / taken-to (date range, from EXIF),
// location, thumbnail (cover) and member-of on its images.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/io/fhclient"
	"github.com/uidbz/tie/metadata"
)

// Shared media relations.
const (
	RelMemberOf    = "member-of"
	RelAddedAt     = "added-at"
	RelMemberCount = "member-count"
	RelThumbnail   = "thumbnail"
	RelRating      = "rating"

	// Audio.
	RelAlbumArtist = "album-artist"
	RelGenre       = "genre"
	RelComposer    = "composer"
	RelDisc        = "disc"
	RelDiscTotal   = "disc-total"
	RelTrackTotal  = "track-total"
	RelDuration    = "duration"
	RelTrackCount  = "track-count"
	RelDiscCount   = "disc-count"

	// Statistics (history.go).
	RelPlayCount   = "play-count"
	RelSkipCount   = "skip-count"
	RelFirstPlayed = "first-played"
	RelLastPlayed  = "last-played"
	RelLastSkipped = "last-skipped"

	// Video / pictures (vocabulary only).
	RelRuntime        = "runtime"
	RelRole           = "role"
	RelSeason         = "season"
	RelEpisode        = "episode"
	RelEpisodeTitle   = "episode-title"
	RelAirDate        = "air-date"
	RelResumePosition = "resume-position"
	RelTakenFrom      = "taken-from"
	RelTakenTo        = "taken-to"
	RelLocation       = "location"
	RelTMDBID         = "tmdb-id"
	RelIMDBID         = "imdb-id"
	RelTVDBID         = "tvdb-id"
)

// Collection kinds beyond the TieType built-ins (audio-dir, image-dir, …).
const (
	TypePlaylist  = "playlist"
	TypeDisc      = "disc"
	TypeMovie     = "movie"
	TypeTVSeries  = "tv-series"
	TypeTVSeason  = "tv-season"
	TypePlayEvent = "play-event"
)

// MediaTimeFormat is the layout of every media datetime relation (added-at,
// played-at, last-played, …): RFC3339 in UTC with second precision. A fixed
// width makes plain string order chronological, so server-side SortByValue
// needs no numeric mode.
const MediaTimeFormat = "2006-01-02T15:04:05Z"

// FormatMediaTime renders t in MediaTimeFormat (UTC).
func FormatMediaTime(t time.Time) string { return t.UTC().Format(MediaTimeFormat) }

// ParseMediaTime parses a media datetime. It also accepts full RFC3339 and the
// legacy tag-date layout, so values written by older clients still read.
func ParseMediaTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// MediaTimeFormat's trailing Z is a literal, so it must parse as UTC;
	// RFC3339 carries its own offset; the legacy tag-date layouts are local.
	for _, l := range []struct {
		layout string
		loc    *time.Location
	}{{MediaTimeFormat, time.UTC}, {time.RFC3339Nano, time.UTC}, {tagDateFormat, time.Local}, {time.DateTime, time.Local}} {
		if t, err := time.ParseInLocation(l.layout, s, l.loc); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// MediaValueTypes are the value-type declarations of the media vocabulary.
var MediaValueTypes = map[string]ValueType{
	str(TieYear):      ValueTypeInt,
	str(TieTrack):     ValueTypeInt,
	RelTrackTotal:     ValueTypeInt,
	RelDisc:           ValueTypeInt,
	RelDiscTotal:      ValueTypeInt,
	RelDuration:       ValueTypeFloat,
	RelTrackCount:     ValueTypeInt,
	RelDiscCount:      ValueTypeInt,
	RelMemberCount:    ValueTypeInt,
	RelRating:         ValueTypeInt,
	RelAddedAt:        ValueTypeDatetime,
	RelPlayCount:      ValueTypeInt,
	RelSkipCount:      ValueTypeInt,
	RelFirstPlayed:    ValueTypeDatetime,
	RelLastPlayed:     ValueTypeDatetime,
	RelLastSkipped:    ValueTypeDatetime,
	RelPlayedAt:       ValueTypeDatetime,
	RelListened:       ValueTypeFloat,
	RelPlayPosition:   ValueTypeFloat,
	RelRuntime:        ValueTypeFloat,
	RelSeason:         ValueTypeInt,
	RelEpisode:        ValueTypeInt,
	RelAirDate:        ValueTypeDate,
	RelResumePosition: ValueTypeFloat,
	RelTakenFrom:      ValueTypeDatetime,
	RelTakenTo:        ValueTypeDatetime,
}

// schemaEnsured remembers, per triplestore collection, that the media value
// types are declared, so EnsureMediaSchema costs one Get per process.
var schemaEnsured sync.Map

// EnsureMediaSchema declares any media value type the collection lacks. It is
// cheap after the first call (cached per collection) and safe to call from
// every writer of media relations.
func (tc *TieClient) EnsureMediaSchema() error {
	key := tc.active.TripleStoreURL + "|" + tc.active.Namespace + "|" + tc.active.Collection
	if _, ok := schemaEnsured.Load(key); ok {
		return nil
	}
	have, err := tc.ValueTypes()
	if err != nil {
		return err
	}
	missing := map[string]ValueType{}
	for rel, t := range MediaValueTypes {
		if have[rel] != t {
			missing[rel] = t
		}
	}
	if err := tc.SetValueTypes(missing); err != nil {
		return err
	}
	schemaEnsured.Store(key, true)
	return nil
}

// IsCollectionLabel reports whether a tie-type value marks a media collection
// (as opposed to the structural directory/file markers, a disc subdirectory,
// or a file's own media type).
func IsCollectionLabel(t string) bool {
	switch t {
	case "", str(TieDirectory), str(TieFile), TypeDisc, TypePlayEvent:
		return false
	}
	return !strings.HasSuffix(t, "-file")
}

// collectionLabels returns a row's collection labels (see IsCollectionLabel).
func collectionLabels(row Row) []string {
	var out []string
	for _, t := range RowValues(row, str(TieTypeProperty)) {
		if IsCollectionLabel(t) {
			out = append(out, t)
		}
	}
	return out
}

// isAudioCollection reports whether labels classify an audio collection.
func isAudioCollection(labels []string) bool {
	for _, l := range labels {
		if strings.HasPrefix(l, "audio-") || l == TypePlaylist {
			return true
		}
	}
	return false
}

// isMediaFileRow reports whether row is a media file a collection can hold.
func isMediaFileRow(row Row) bool {
	for _, t := range RowValues(row, str(TieTypeProperty)) {
		switch t {
		case str(TieImageFile), str(TieAudioFile), str(TieVideoFile), str(TieDocumentFile),
			str(TieImageArchive), str(TieAudioArchive), str(TieVideoArchive), str(TieDocumentArchive):
			return true
		}
	}
	return strings.HasPrefix(RowFirst(row, str(TieMediaType)), "audio/")
}

// isAudioRow reports whether row is an audio file.
func isAudioRow(row Row) bool {
	return RowHas(row, str(TieTypeProperty), str(TieAudioFile)) ||
		strings.HasPrefix(RowFirst(row, str(TieMediaType)), "audio/")
}

// IndexResult summarizes what IndexCollection changed.
type IndexResult struct {
	Collection   DirUID
	Members      int // media members found below the collection
	AddedMembers int // member-of edges written
	DroppedStale int // stale member-of edges removed
	Discs        int // disc subdirectories labeled
	Ops          int // total write ops (0 = already up to date)
}

// IndexOptions tunes IndexCollection.
type IndexOptions struct {
	// DryRun computes the result without writing anything.
	DryRun bool
	// Collection selects the triplestore collection ("" = the client's).
	Collection string
}

var discDirRe = regexp.MustCompile(`(?i)^(?:cd|disc|disk)\s*[-_.]?\s*(\d{1,2})$`)

// discNumberFromName parses a disc-like directory name (cd2, Disc 3, disk-1).
func discNumberFromName(name string) int {
	m := discDirRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// childRows returns the expanded rows of every child (files and subdirs) of a
// directory via its reverse parent edges, in the client's scoped collection.
func (tc *TieClient) childRows(uid string) ([]Row, error) {
	rows, _, err := tc.Query(QuerySpec{Terms: []string{uid}, Reverse: true, Filter: str(TieParent), Expand: true, Limit: -1})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return rows, err
}

// CollectionMembers returns the expanded rows of every subject with a
// member-of edge to uid (one round trip). An empty result is not an error.
func (tc *TieClient) CollectionMembers(uid string) ([]Row, error) {
	rows, _, err := tc.Query(QuerySpec{Terms: []string{uid}, Reverse: true, Filter: RelMemberOf, Expand: true, Limit: -1})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return rows, err
}

// memberInfo is one member found by the walk, with the disc it sits on.
type memberInfo struct {
	row  Row
	disc int // from the enclosing disc directory, 0 = none
}

// walkCollection gathers the media members below a collection directory,
// descending into plain subdirectories (disc folders, artwork/, …) but not
// into nested collections (their members belong to them) or version-history
// "_prev" directories. It also returns the disc subdirectories it saw.
func (tc *TieClient) walkCollection(root string) (members []memberInfo, discDirs map[string]int, err error) {
	discDirs = map[string]int{}
	type item struct {
		uid  string
		disc int
	}
	queue := []item{{uid: root}}
	seen := map[string]bool{root: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rows, err := tc.childRows(cur.uid)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			if row.Key == cur.uid || seen[row.Key] {
				continue
			}
			if RowHas(row, str(TieTypeProperty), str(TieDirectory)) && len(RowValues(row, str(TiePath))) > 0 {
				seen[row.Key] = true
				name := RowFirst(row, str(TieName))
				if name == "" {
					name = path.Base(RowFirst(row, str(TiePath)))
				}
				if strings.HasSuffix(name, "_prev") || len(collectionLabels(row)) > 0 {
					continue
				}
				disc := cur.disc
				if n := discNumberFromName(name); n > 0 && cur.uid == root {
					disc = n
				}
				if disc > 0 && cur.uid == root {
					discDirs[row.Key] = disc
				}
				queue = append(queue, item{uid: row.Key, disc: disc})
				continue
			}
			if isMediaFileRow(row) {
				seen[row.Key] = true
				members = append(members, memberInfo{row: row, disc: cur.disc})
			}
		}
	}
	return members, discDirs, nil
}

// modeString returns the most common non-empty value (ties: lexically first).
func modeString(values []string) string {
	counts := map[string]int{}
	for _, v := range values {
		if v != "" {
			counts[v]++
		}
	}
	best, bestN := "", 0
	for v, n := range counts {
		if n > bestN || (n == bestN && v < best) {
			best, bestN = v, n
		}
	}
	return best
}

// topGenres returns up to n genres by frequency across members.
func topGenres(rows []Row, n int) []string {
	counts := map[string]int{}
	spelling := map[string]string{}
	for _, r := range rows {
		for _, g := range RowValues(r, RelGenre) {
			k := strings.ToLower(strings.TrimSpace(g))
			if k == "" {
				continue
			}
			counts[k]++
			if _, ok := spelling[k]; !ok {
				spelling[k] = strings.TrimSpace(g)
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = spelling[k]
	}
	return out
}

// setIfChanged queues Set(key, rel, values) unless row already holds exactly
// values (order-insensitive). It reports whether an op was queued.
func setIfChanged(b *api.Batch, row Row, key, rel string, values []string) bool {
	have := slices.Clone(RowValues(row, rel))
	want := slices.Clone(values)
	sort.Strings(have)
	sort.Strings(want)
	if slices.Equal(have, want) {
		return false
	}
	b.Set(key, rel, values)
	return true
}

// addedAtFor picks the added-at value for a subject lacking one: its
// tag-date (the import time, as close to "first added" as old data allows),
// else now.
func addedAtFor(row Row, now time.Time) string {
	if t, ok := ParseMediaTime(RowFirst(row, str(TieTagDate))); ok {
		return FormatMediaTime(t)
	}
	return FormatMediaTime(now)
}

// IndexCollection brings a collection directory up to the collection
// contract: it walks the subtree (descending into disc and other plain
// subfolders, not into nested collections), writes member-of from every media
// member (dropping stale edges of members no longer below it), stamps
// added-at where absent (on the collection and on members), labels disc
// subdirectories, and — for audio collections — writes the album summary
// (album-artist, genre, track-count, disc-count, duration; album/artist/year
// when the root lacks them, as multi-disc roots do since no file sits
// directly in them). It is idempotent: an up-to-date collection costs reads
// only (Ops == 0).
func (tc *TieClient) IndexCollection(uid DirUID, opts IndexOptions) (IndexResult, error) {
	res := IndexResult{Collection: uid}
	// Everything below addresses the collection the options name: reads, the
	// walk and the batch cannot end up in different ones (§3.8).
	tc = tc.In(opts.Collection)
	root, err := tc.Get(string(uid))
	if err != nil {
		return res, fmt.Errorf("index %s: %w", uid, err)
	}
	if RowHas(root, str(TieTypeProperty), TypePlaylist) {
		return res, nil // a playlist's membership is its explicit track list (playlist.go)
	}
	labels := collectionLabels(root)
	members, discDirs, err := tc.walkCollection(string(uid))
	if err != nil {
		return res, fmt.Errorf("index %s: %w", uid, err)
	}
	res.Members = len(members)
	now := time.Now()
	b := tc.NewBatch()

	inWalk := map[string]bool{}
	var memberRows []Row
	earliest := ""
	for _, m := range members {
		row := m.row
		inWalk[row.Key] = true
		memberRows = append(memberRows, row)
		if !RowHas(row, RelMemberOf, string(uid)) {
			b.Add(row.Key, RelMemberOf, string(uid))
			res.AddedMembers++
		}
		added := RowFirst(row, RelAddedAt)
		if added == "" {
			added = addedAtFor(row, now)
			b.Set(row.Key, RelAddedAt, []string{added})
		}
		if t, ok := ParseMediaTime(added); ok {
			if s := FormatMediaTime(t); earliest == "" || s < earliest {
				earliest = s
			}
		}
		// A track below a disc folder without its own DISCNUMBER tag gets the
		// folder's disc number, so readers can order discs uniformly.
		if m.disc > 0 && isAudioRow(row) && RowFirst(row, RelDisc) == "" {
			b.Set(row.Key, RelDisc, []string{strconv.Itoa(m.disc)})
		}
	}

	// Stale edges: members that left the collection (moved, deleted). Needs the
	// reverse member-of index; an older triplestore without it yields nothing
	// here, which merely leaves stale edges in place.
	if existing, err := tc.CollectionMembers(string(uid)); err == nil {
		for _, row := range existing {
			if !inWalk[row.Key] {
				b.Delete(row.Key, RelMemberOf, string(uid))
				res.DroppedStale++
			}
		}
	}

	if RowFirst(root, RelAddedAt) == "" {
		v := earliest
		if v == "" {
			v = addedAtFor(root, now)
		}
		b.Set(string(uid), RelAddedAt, []string{v})
	}
	setIfChanged(b, root, string(uid), RelMemberCount, []string{strconv.Itoa(len(members))})

	if isAudioCollection(labels) {
		var audio []Row
		for _, r := range memberRows {
			if isAudioRow(r) {
				audio = append(audio, r)
			}
		}
		discs := map[int]bool{}
		var total float64
		var albums, artists, albumArtists, years []string
		for _, r := range audio {
			if d, _ := strconv.Atoi(RowFirst(r, RelDisc)); d > 0 {
				discs[d] = true
			}
			if f, err := strconv.ParseFloat(RowFirst(r, RelDuration), 64); err == nil {
				total += f
			}
			albums = append(albums, RowFirst(r, str(TieAlbum)))
			artists = append(artists, RowFirst(r, str(TieArtist)))
			albumArtists = append(albumArtists, RowFirst(r, RelAlbumArtist))
			years = append(years, RowFirst(r, str(TieYear)))
		}
		for _, d := range discDirs {
			discs[d] = true
		}
		discCount := len(discs)
		if discCount == 0 && len(audio) > 0 {
			discCount = 1
		}
		setIfChanged(b, root, string(uid), RelTrackCount, []string{strconv.Itoa(len(audio))})
		setIfChanged(b, root, string(uid), RelDiscCount, []string{strconv.Itoa(discCount)})
		if total > 0 {
			setIfChanged(b, root, string(uid), RelDuration, []string{strconv.FormatFloat(total, 'f', 3, 64)})
		}
		if g := topGenres(audio, 3); len(g) > 0 {
			setIfChanged(b, root, string(uid), RelGenre, g)
		}
		albumArtist := modeString(albumArtists)
		if albumArtist == "" {
			if distinct := uniqueNonEmpty(artists); len(distinct) > 1 {
				albumArtist = "Various Artists"
			} else {
				albumArtist = modeString(artists)
			}
		}
		if albumArtist != "" {
			setIfChanged(b, root, string(uid), RelAlbumArtist, []string{albumArtist})
		}
		if RowFirst(root, str(TieAlbum)) == "" {
			if a := uniqueNonEmpty(albums); len(a) == 1 {
				b.Set(string(uid), str(TieAlbum), a)
			}
		}
		if RowFirst(root, str(TieArtist)) == "" && albumArtist != "" {
			b.Set(string(uid), str(TieArtist), []string{albumArtist})
		}
		if RowFirst(root, str(TieYear)) == "" {
			if y := modeString(years); y != "" {
				b.Set(string(uid), str(TieYear), []string{y})
			}
		}
		if len(discDirs) > 0 {
			ids := make([]string, 0, len(discDirs))
			for id := range discDirs {
				ids = append(ids, id)
			}
			rows, err := tc.Expand(ids)
			if err != nil {
				return res, fmt.Errorf("index %s: %w", uid, err)
			}
			for _, r := range rows {
				n := discDirs[r.Key]
				if !RowHas(r, str(TieTypeProperty), TypeDisc) {
					b.Add(r.Key, str(TieTypeProperty), TypeDisc)
					res.Discs++
				}
				setIfChanged(b, r, r.Key, RelDisc, []string{strconv.Itoa(n)})
			}
		}
	}

	res.Ops = len(b.Ops)
	if opts.DryRun || res.Ops == 0 {
		return res, nil
	}
	if err := tc.EnsureMediaSchema(); err != nil {
		return res, err
	}
	if _, err := tc.Batch(b); err != nil {
		return res, fmt.Errorf("index %s: %w", uid, err)
	}
	return res, nil
}

func uniqueNonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// EnsureAddedAt stamps added-at on subject unless it already has one.
func (tc *TieClient) EnsureAddedAt(subject string) error {
	row, err := tc.Get(subject)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if RowFirst(row, RelAddedAt) != "" {
		return nil
	}
	b := tc.NewBatch()
	b.Set(subject, RelAddedAt, []string{addedAtFor(row, time.Now())})
	_, err = tc.Batch(b)
	return err
}

// BackfillOptions tunes Backfill.
type BackfillOptions struct {
	DryRun bool
	// Types limits which collection labels are indexed (nil = every
	// collection label in use plus the built-in *-dir types).
	Types []string
	// Progress, when non-nil, is called after each collection.
	Progress func(done, total int, r IndexResult, err error)
}

// BackfillResult summarizes a Backfill run.
type BackfillResult struct {
	Collections int
	Changed     int
	Archives    int // archive collections stamped with added-at
	Errors      []error
}

// Backfill indexes every collection in the store (see IndexCollection), so
// data imported before the collection contract existed gains member-of,
// added-at, disc labels and album summaries. Archive collections (one blob,
// e.g. audio-archive) only gain added-at. It reads only triples: track
// fields the old import never wrote (genre, disc, composer) need
// RescanAudio.
func (tc *TieClient) Backfill(opts BackfillOptions) (BackfillResult, error) {
	var res BackfillResult
	types := opts.Types
	if types == nil {
		all, err := tc.ListDirTypes()
		if err != nil {
			return res, err
		}
		for _, t := range all {
			if IsCollectionLabel(t) && t != TypePlaylist {
				types = append(types, t)
			}
		}
	}
	var dirs []string
	archives := map[string]Row{}
	seen := map[string]bool{}
	for _, t := range types {
		rows, err := tc.allOfType(t)
		if err != nil {
			return res, err
		}
		for _, r := range rows {
			if seen[r.Key] {
				continue
			}
			seen[r.Key] = true
			if strings.HasSuffix(t, "-archive") && len(RowValues(r, str(TiePath))) == 0 {
				archives[r.Key] = r
				continue
			}
			dirs = append(dirs, r.Key)
		}
	}
	sort.Strings(dirs)
	res.Collections = len(dirs)
	for i, d := range dirs {
		r, err := tc.IndexCollection(DirUID(d), IndexOptions{DryRun: opts.DryRun})
		if err != nil {
			res.Errors = append(res.Errors, err)
		} else if r.Ops > 0 {
			res.Changed++
		}
		if opts.Progress != nil {
			opts.Progress(i+1, len(dirs), r, err)
		}
	}
	if len(archives) > 0 {
		b := tc.NewBatch()
		now := time.Now()
		for key, r := range archives {
			if RowFirst(r, RelAddedAt) == "" {
				b.Set(key, RelAddedAt, []string{addedAtFor(r, now)})
				res.Archives++
			}
		}
		if !opts.DryRun && len(b.Ops) > 0 {
			if err := tc.EnsureMediaSchema(); err != nil {
				return res, err
			}
			if _, err := tc.Batch(b); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

// RelMetaVersion marks which revision of the per-track metadata extraction a
// file's triples reflect. MetaVersion 2 = the media vocabulary (genre, disc,
// disc-total, track-total, composer). RescanAudio skips files already at it.
const (
	RelMetaVersion = "meta-version"
	MetaVersion    = "2"
)

// RescanOptions tunes RescanAudio.
type RescanOptions struct {
	DryRun  bool
	Force   bool // rescan files already at MetaVersion
	Workers int  // concurrent downloads (default 8)
	// Progress, when non-nil, is called after each file.
	Progress func(done, total int, hash string, err error)
}

// rescanHeadBytes is how much of a blob RescanAudio fetches first: tags live
// at the start of FLAC/Vorbis/MP4/ID3v2 files. A cut-off parse (huge
// embedded art) falls back to the whole blob.
const rescanHeadBytes = 2 << 20

// RescanAudio re-reads the embedded tags of every audio file whose triples
// predate MetaVersion — fetching only the head of each blob via an HTTP range
// request where possible — and writes the fields older imports never stored
// (genre, disc, disc-total, track-total, composer). It returns the number of
// files updated.
func (tc *TieClient) RescanAudio(opts RescanOptions) (int, error) {
	host, err := tc.ResolveHost("")
	if err != nil {
		return 0, err
	}
	rows, err := tc.allOfType(str(TieAudioFile))
	if err != nil {
		return 0, err
	}
	var todo []Row
	for _, r := range rows {
		if opts.Force || RowFirst(r, RelMetaVersion) != MetaVersion {
			todo = append(todo, r)
		}
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = 8
	}
	fh := FileHostClient(host)
	type result struct {
		hash string
		ops  func(b *api.Batch)
		err  error
	}
	jobs := make(chan Row)
	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				m, err := fetchAudioMeta(fh, r.Key)
				res := result{hash: r.Key, err: err}
				if err == nil {
					row := r
					res.ops = func(b *api.Batch) {
						k := row.Key
						if m.TrackTotal > 0 {
							setIfChanged(b, row, k, RelTrackTotal, []string{strconv.Itoa(m.TrackTotal)})
						}
						if m.Disc > 0 {
							setIfChanged(b, row, k, RelDisc, []string{strconv.Itoa(m.Disc)})
						}
						if m.DiscTotal > 0 {
							setIfChanged(b, row, k, RelDiscTotal, []string{strconv.Itoa(m.DiscTotal)})
						}
						if g := SplitGenres(m.Genre); len(g) > 0 {
							setIfChanged(b, row, k, RelGenre, g)
						}
						if m.Composer != "" {
							setIfChanged(b, row, k, RelComposer, []string{m.Composer})
						}
						if m.AlbumArtist != "" && RowFirst(row, RelAlbumArtist) == "" {
							b.Set(k, RelAlbumArtist, []string{m.AlbumArtist})
						}
						if m.Duration > 0 && RowFirst(row, RelDuration) == "" {
							b.Set(k, RelDuration, []string{strconv.FormatFloat(m.Duration, 'f', 3, 64)})
						}
						b.Set(k, RelMetaVersion, []string{MetaVersion})
					}
				}
				results <- res
			}
		}()
	}
	go func() {
		for _, r := range todo {
			jobs <- r
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	b := tc.NewBatch()
	updated, done := 0, 0
	var firstErr error
	for res := range results {
		done++
		if res.err == nil {
			res.ops(b)
			updated++
		} else if firstErr == nil {
			firstErr = res.err
		}
		if opts.Progress != nil {
			opts.Progress(done, len(todo), res.hash, res.err)
		}
		if !opts.DryRun && len(b.Ops) >= historyFlush {
			if _, err := tc.Batch(b); err != nil {
				return updated, err
			}
			b = tc.NewBatch()
		}
	}
	if !opts.DryRun && len(b.Ops) > 0 {
		if err := tc.EnsureMediaSchema(); err != nil {
			return updated, err
		}
		if _, err := tc.Batch(b); err != nil {
			return updated, err
		}
	}
	return updated, firstErr
}

// fetchAudioMeta reads a blob's audio tags, trying a ranged head fetch before
// the whole blob. A server that ignores Range hands back the whole body, so no
// second request is needed.
func fetchAudioMeta(fh *fhclient.Client, hash string) (metadata.Media, error) {
	data, partial, err := fh.GetRange(hash, rescanHeadBytes)
	if err != nil {
		return metadata.Media{}, err
	}
	m := ExtractMediaMetadataFrom(bytes.NewReader(data))
	if partial && m.Title == "" && m.Album == "" && m.Artist == "" {
		body, err := fh.Get(hash)
		if err != nil {
			return metadata.Media{}, err
		}
		defer body.Close()
		data, err = io.ReadAll(body)
		if err != nil {
			return metadata.Media{}, err
		}
		m = ExtractMediaMetadataFrom(bytes.NewReader(data))
	}
	return m, nil
}
