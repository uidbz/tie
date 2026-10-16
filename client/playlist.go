package client

// playlist.go makes playlists first-class: a playlist is its own subject, not
// a directory.
//
//	(P, tie-type,       playlist)
//	(P, name,           <name>)          (also album, for older readers)
//	(P, playlist-track, "000000:<hash>") ordered entries; the zero-padded
//	                                     ordinal restores the order from the
//	                                     store's sorted multi-values
//	(P, track-count,    <n>)
//	(P, added-at / updated-at, RFC3339)
//	(P, tag,            playlist)        + any user tags
//	(<hash>, member-of, P)               one per distinct track
//
// Tracks keep their real parent (album) edge. Playlists written by tie-audio
// before this existed were audio-dirs that added a fake (hash, parent, P) edge
// per track — polluting the directory tree and making a track's album resolve
// to the playlist; MigrateLegacyPlaylists converts them.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// RelPlaylistTrack holds a playlist's ordered entries.
	RelPlaylistTrack = "playlist-track"
	RelUpdatedAt     = "updated-at"
	// PlaylistTag is the tag every playlist carries, so tag-driven views can
	// list playlists without knowing about the type.
	PlaylistTag = "playlist"
)

// Playlist is a parsed playlist subject.
type Playlist struct {
	UID       string
	Name      string
	Tracks    []string // content hashes, in order
	Tags      []string
	Thumbnail string
	AddedAt   time.Time
	UpdatedAt time.Time
	// Legacy marks a pre-v0.6 playlist (an audio-dir with fake parent edges);
	// MigrateLegacyPlaylists converts it.
	Legacy bool
}

// PlaylistEntry renders the i-th ordered entry for hash.
func PlaylistEntry(i int, hash string) string { return fmt.Sprintf("%06d:%s", i, hash) }

// PlaylistTracks parses a row's ordered playlist-track entries.
func PlaylistTracks(r Row) []string {
	entries := append([]string(nil), RowValues(r, RelPlaylistTrack)...)
	sort.Strings(entries)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if i := strings.IndexByte(e, ':'); i >= 0 {
			e = e[i+1:]
		}
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// IsPlaylistRow reports whether r is a playlist, current or legacy.
func IsPlaylistRow(r Row) bool {
	return RowHas(r, str(TieTypeProperty), TypePlaylist) || isLegacyPlaylistRow(r)
}

func isLegacyPlaylistRow(r Row) bool {
	return !RowHas(r, str(TieTypeProperty), TypePlaylist) && len(RowValues(r, RelPlaylistTrack)) > 0
}

// PlaylistFromRow parses an expanded playlist row.
func PlaylistFromRow(r Row) Playlist {
	p := Playlist{
		UID:       r.Key,
		Name:      RowFirst(r, str(TieName)),
		Tracks:    PlaylistTracks(r),
		Tags:      RowValues(r, str(TieTag)),
		Thumbnail: RowFirst(r, RelThumbnail),
		Legacy:    isLegacyPlaylistRow(r),
	}
	if p.Name == "" {
		p.Name = RowFirst(r, str(TieAlbum))
	}
	p.AddedAt, _ = ParseMediaTime(RowFirst(r, RelAddedAt))
	if p.AddedAt.IsZero() {
		p.AddedAt, _ = ParseMediaTime(RowFirst(r, str(TieTagDate)))
	}
	p.UpdatedAt, _ = ParseMediaTime(RowFirst(r, RelUpdatedAt))
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.AddedAt
	}
	return p
}

func newSubjectUID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("client: reading random bytes: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func distinct(hashes []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hashes {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// CreatePlaylist stores a new playlist of hashes (in order; duplicates kept)
// and returns its uid. tags are added besides the implicit "playlist" tag.
func (tc *TieClient) CreatePlaylist(name string, hashes []string, tags []string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("create playlist: empty name")
	}
	uid := newSubjectUID()
	now := FormatMediaTime(time.Now())
	b := tc.NewBatch()
	b.Add(uid, str(TieTypeProperty), TypePlaylist)
	b.Set(uid, str(TieName), []string{name})
	b.Set(uid, str(TieAlbum), []string{name})
	b.Set(uid, RelAddedAt, []string{now})
	b.Set(uid, RelUpdatedAt, []string{now})
	b.Set(uid, RelTrackCount, []string{strconv.Itoa(len(hashes))})
	b.Set(uid, str(TieTagDate), []string{time.Now().Format(tagDateFormat)})
	for _, t := range append([]string{PlaylistTag}, tags...) {
		if t = strings.TrimSpace(t); t != "" {
			b.Add(uid, str(TieTag), t)
			b.Add(str(TieTags), str(TieAll), t)
		}
	}
	entries := make([]string, len(hashes))
	for i, h := range hashes {
		entries[i] = PlaylistEntry(i, h)
	}
	b.Set(uid, RelPlaylistTrack, entries)
	for _, h := range distinct(hashes) {
		b.Add(h, RelMemberOf, uid)
	}
	b.Add(typeRegistrySubject, str(TieAll), TypePlaylist)
	if err := tc.EnsureMediaSchema(); err != nil {
		return "", err
	}
	if _, err := tc.Batch(b); err != nil {
		return "", fmt.Errorf("create playlist: %w", err)
	}
	return uid, nil
}

// ReadPlaylist fetches one playlist.
func (tc *TieClient) ReadPlaylist(uid string) (Playlist, error) {
	r, err := tc.Get(uid)
	if err != nil {
		return Playlist{}, err
	}
	if !IsPlaylistRow(r) {
		return Playlist{}, fmt.Errorf("%s is not a playlist", uid)
	}
	return PlaylistFromRow(r), nil
}

// ListPlaylists returns every playlist (current and legacy), most recently
// updated first.
func (tc *TieClient) ListPlaylists() ([]Playlist, error) {
	byUID := map[string]Playlist{}
	for _, spec := range []QuerySpec{
		{Terms: []string{TypePlaylist}, Filter: str(TieTypeProperty), Reverse: true, Expand: true, Limit: -1},
		{Terms: []string{PlaylistTag}, Filter: str(TieTag), Reverse: true, Expand: true, Limit: -1},
	} {
		rows, _, err := tc.Query(spec)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		for _, r := range rows {
			if IsPlaylistRow(r) {
				byUID[r.Key] = PlaylistFromRow(r)
			}
		}
	}
	out := make([]Playlist, 0, len(byUID))
	for _, p := range byUID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].UID < out[j].UID
	})
	return out, nil
}

// SetPlaylistTracks replaces a playlist's entries with hashes (in order) and
// diffs its member-of edges.
func (tc *TieClient) SetPlaylistTracks(uid string, hashes []string) error {
	p, err := tc.ReadPlaylist(uid)
	if err != nil {
		return err
	}
	if p.Legacy {
		if _, err := tc.migratePlaylists([]Playlist{p}); err != nil {
			return err
		}
	}
	b := tc.NewBatch()
	entries := make([]string, len(hashes))
	for i, h := range hashes {
		entries[i] = PlaylistEntry(i, h)
	}
	b.Set(uid, RelPlaylistTrack, entries)
	b.Set(uid, RelTrackCount, []string{strconv.Itoa(len(hashes))})
	b.Set(uid, RelUpdatedAt, []string{FormatMediaTime(time.Now())})
	want := map[string]bool{}
	for _, h := range distinct(hashes) {
		want[h] = true
	}
	had := map[string]bool{}
	for _, h := range distinct(p.Tracks) {
		had[h] = true
		if !want[h] {
			b.Delete(h, RelMemberOf, uid)
		}
	}
	for h := range want {
		if !had[h] {
			b.Add(h, RelMemberOf, uid)
		}
	}
	if _, err := tc.Batch(b); err != nil {
		return fmt.Errorf("set playlist tracks: %w", err)
	}
	return nil
}

// RenamePlaylist changes a playlist's name.
func (tc *TieClient) RenamePlaylist(uid, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("rename playlist: empty name")
	}
	if _, err := tc.ReadPlaylist(uid); err != nil {
		return err
	}
	b := tc.NewBatch()
	b.Set(uid, str(TieName), []string{name})
	b.Set(uid, str(TieAlbum), []string{name})
	b.Set(uid, RelUpdatedAt, []string{FormatMediaTime(time.Now())})
	_, err := tc.Batch(b)
	return err
}

// DeletePlaylist removes a playlist subject and its member-of edges (and a
// legacy playlist's fake parent edges). The tracks are untouched.
func (tc *TieClient) DeletePlaylist(uid string) error {
	r, err := tc.Get(uid)
	if err != nil {
		return err
	}
	if !IsPlaylistRow(r) {
		return fmt.Errorf("%s is not a playlist", uid)
	}
	b := tc.NewBatch()
	for _, h := range distinct(PlaylistTracks(r)) {
		b.Delete(h, RelMemberOf, uid)
		if isLegacyPlaylistRow(r) {
			b.Delete(h, str(TieParent), uid)
		}
	}
	for rel, vals := range r.Attributes {
		for _, v := range vals {
			b.Delete(uid, rel, v)
		}
	}
	if _, err := tc.Batch(b); err != nil {
		return fmt.Errorf("delete playlist: %w", err)
	}
	return nil
}

// MigrateLegacyPlaylists converts every legacy playlist (an audio-dir with
// playlist-track entries and fake per-track parent edges) to the playlist
// type: the fake parent edges and the audio-dir/directory labels go, member-of
// edges, added-at and track-count are written. It returns how many were
// converted.
func (tc *TieClient) MigrateLegacyPlaylists() (int, error) {
	all, err := tc.ListPlaylists()
	if err != nil {
		return 0, err
	}
	var legacy []Playlist
	for _, p := range all {
		if p.Legacy {
			legacy = append(legacy, p)
		}
	}
	return tc.migratePlaylists(legacy)
}

func (tc *TieClient) migratePlaylists(legacy []Playlist) (int, error) {
	if len(legacy) == 0 {
		return 0, nil
	}
	if err := tc.EnsureMediaSchema(); err != nil {
		return 0, err
	}
	b := tc.NewBatch()
	for _, p := range legacy {
		uid := p.UID
		for _, h := range distinct(p.Tracks) {
			b.Delete(h, str(TieParent), uid)
			b.Add(h, RelMemberOf, uid)
		}
		b.Delete(uid, str(TieTypeProperty), str(TieAudioDir))
		b.Delete(uid, str(TieTypeProperty), str(TieDirectory))
		b.Add(uid, str(TieTypeProperty), TypePlaylist)
		added := p.AddedAt
		if added.IsZero() {
			added = time.Now()
		}
		b.Set(uid, RelAddedAt, []string{FormatMediaTime(added)})
		updated := p.UpdatedAt
		if updated.IsZero() {
			updated = added
		}
		b.Set(uid, RelUpdatedAt, []string{FormatMediaTime(updated)})
		b.Set(uid, RelTrackCount, []string{strconv.Itoa(len(p.Tracks))})
		b.Set(uid, str(TieFilesize), nil) // legacy abused filesize as the track count
		if len(b.Ops) >= historyFlush {
			if _, err := tc.Batch(b); err != nil {
				return 0, err
			}
			b = tc.NewBatch()
		}
	}
	b.Add(typeRegistrySubject, str(TieAll), TypePlaylist)
	if _, err := tc.Batch(b); err != nil {
		return 0, fmt.Errorf("migrate playlists: %w", err)
	}
	return len(legacy), nil
}
