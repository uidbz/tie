package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~uid/tie/api"
	"git.sr.ht/~uid/tie/io/putlib"
	"git.sr.ht/~uid/tie/metadata"
)

//go:generate stringer -type=TieType -linecomment
//go:generate stringer -type=TieProperty -linecomment

type TieType int
type TieProperty int
type TieCategory int
type DirUID string

const (
	TieUnknownFile     TieType = iota // unknown-file
	TieImageFile                      // image-file
	TieAudioFile                      // audio-file
	TieVideoFile                      // video-file
	TieDocumentFile                   // document-file
	TieArchiveFile                    // archive-file
	TieImageDir                       // image-dir
	TieAudioDir                       // audio-dir
	TieVideoDir                       // video-dir
	TieDocumentDir                    // document-dir
	TieImageArchive                   // image-archive
	TieAudioArchive                   // audio-archive
	TieVideoArchive                   // video-archive
	TieDocumentArchive                // document-archive
	TieDirectory                      // directory
	TieFile                           // file
)

const (
	TieUid          TieProperty = iota // tie-uid
	TieFilename                        // filename
	TieFilesize                        // filesize
	TieName                            // name
	TieMediaType                       // media-type
	TieFileHost                        // filehost
	TieTag                             // tag
	TiePath                            // path
	TieParent                          // parent
	TieTags                            // tags
	TieTagDate                         // tag-date
	TieCollection                      // collection
	TieTypeProperty                    // tie-type
	TieAll                             // all
	TieTitle                           // title
	TieArtist                          // artist
	TieAlbum                           // album
	TieYear                            // year
	TieTrack                           // track
	TieTiedirHash                      // tiedir-hash
	TieDirUID                          // dir-uid
	TieVersionOf                       // version-of
	TieVersionDate                     // version-date
	TieFavorite                        // favorite
)

// FileURIScheme prefixes the virtual path stored in every (uid, "path", …)
// triple, namespacing the tag/path tree. It is a private URI scheme conforming
// to RFC 3986 generic syntax ("tie" is a valid scheme name; "tie:/a/b" is a
// well-formed URI with no authority) — deliberately NOT the RFC 8089 "file:"
// scheme, since these are tie-internal tree nodes, not local-filesystem paths.
// The value is persisted, so changing it requires migrating existing path
// triples (see cmd/tie migration notes / test-env/migrate-scheme.sh).
const (
	FileURIScheme string = "tie:"
)

// tagDateFormat is the layout for the single-valued tag-date (last import time).
// It carries microsecond precision so several re-imports within the same wall
// second still order correctly for version retention; older second-resolution
// values (written before this) still parse, since the fractional part is
// optional in time.Parse.
const tagDateFormat = "2006-01-02 15:04:05.000000"

func (d DirUID) String() string {
	return string(d)
}

func str(t fmt.Stringer) string {
	return t.String()
}

// tieTypeByName inverts TieType.String() so the name->value mapping stays in
// sync with the stringer output instead of being hand-maintained.
var tieTypeByName = func() map[string]TieType {
	m := make(map[string]TieType)
	for t := TieUnknownFile; t <= TieFile; t++ {
		m[t.String()] = t
	}
	return m
}()

func StringToTieType(t string) TieType {
	if tt, ok := tieTypeByName[t]; ok {
		return tt
	}
	return TieUnknownFile
}

func SliceToTieType(types []string) (t []TieType) {
	for _, x := range types {
		t = append(t, StringToTieType(x))
	}
	return t
}

type TagInfo struct {
	Hash      string
	File      string
	Size      int
	MediaType string
	TieType   TieType
	Tags      []string
	Directory DirUID
	IsDir     bool
	Metadata  metadata.Media
}

// applyArchiveOverride forces an archive file's tie-type to forced when forced
// names an archive type and the sniffed type is itself an archive. Non-archive
// files keep their auto-detected type. This backs `import <archive-type>`, which
// forces the classification of zips (e.g. an album zip that auto-detects as
// image-archive because scans dominate) without disturbing other files.
func applyArchiveOverride(sniffed, forced TieType) TieType {
	if IsArchiveType(forced) && IsArchiveType(sniffed) {
		return forced
	}
	return sniffed
}

func (tie *TieClient) ImportFile(file string, host FileHost, collection string, tags []string, directory DirUID, forcedArchive TieType) error {
	fmt.Println("Importing:", file)
	fileType, err := GetTieTypeFromPath(file)
	if err != nil {
		return err
	}
	fileType = applyArchiveOverride(fileType, forcedArchive)
	stat, err := os.Stat(file)
	if err != nil {
		return err
	}
	status := putlib.Upload(host.URL, file, putlib.PutConfig{Client: HTTPClientFor(host)})

	if status.ErrorMsg == "" {
		info := TagInfo{
			Hash:      status.LastItem.Hash,
			File:      file,
			Size:      int(stat.Size()),
			MediaType: status.LastItem.MediaType,
			Directory: directory,
			TieType:   fileType,
			Tags:      tags,
			IsDir:     stat.IsDir(),
			Metadata:  ExtractMediaMetadata(file),
		}
		return Tag(tie, info, collection)
	}
	return fmt.Errorf("Error uploading: %v\n%v\n", status.LastItem.Filename, status.LastItem.ErrorMsg)
}

// sanitizePathSegment makes a metadata value safe to embed in a single virtual
// path segment: path separators become spaces (so a value can't inject extra
// directory levels), "." and ".." are neutralized, control characters are
// dropped, and surrounding whitespace is trimmed.
func sanitizePathSegment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return ' '
		case r < 0x20:
			return -1
		default:
			return r
		}
	}, s)
	s = strings.TrimSpace(s)
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// renderDestTemplate expands {artist}, {album}, {year}, {title}, {track} in tmpl
// from m, sanitizing each value so it stays within one path segment. It returns
// an error if any referenced variable resolves to an empty value, so the caller
// can fall back rather than produce a path with blank segments. An unknown
// variable name is also an error.
func renderDestTemplate(tmpl string, m metadata.Media) (string, error) {
	value := func(name string) (string, bool) {
		switch name {
		case "artist":
			return sanitizePathSegment(m.Artist), true
		case "album":
			return sanitizePathSegment(m.Album), true
		case "title":
			return sanitizePathSegment(m.Title), true
		case "year":
			if m.Year == 0 {
				return "", true
			}
			return strconv.Itoa(m.Year), true
		case "track":
			if m.Track == 0 {
				return "", true
			}
			return strconv.Itoa(m.Track), true
		}
		return "", false
	}

	var out strings.Builder
	for i := 0; i < len(tmpl); {
		c := tmpl[i]
		if c != '{' {
			out.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(tmpl[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated '{' in import destination template %q", tmpl)
		}
		name := tmpl[i+1 : i+end]
		v, ok := value(name)
		if !ok {
			return "", fmt.Errorf("unknown variable {%s} in import destination template %q", name, tmpl)
		}
		if v == "" {
			return "", fmt.Errorf("empty value for {%s}", name)
		}
		out.WriteString(v)
		i += end + 1
	}
	return out.String(), nil
}

// aggregateMetadata collapses per-file metadata into a single directory-level
// value per field by taking the most common non-empty value. Album/artist/year
// describe the directory (album) as a whole, so the modal value is the stable
// choice even when a stray file carries different tags.
func aggregateMetadata(items []metadata.Media) metadata.Media {
	modeStr := func(get func(metadata.Media) string) string {
		counts := make(map[string]int)
		for _, it := range items {
			if v := get(it); v != "" {
				counts[v]++
			}
		}
		best, bestN := "", 0
		for v, n := range counts {
			if n > bestN {
				best, bestN = v, n
			}
		}
		return best
	}
	modeInt := func(get func(metadata.Media) int) int {
		counts := make(map[int]int)
		for _, it := range items {
			if v := get(it); v != 0 {
				counts[v]++
			}
		}
		best, bestN := 0, 0
		for v, n := range counts {
			if n > bestN {
				best, bestN = v, n
			}
		}
		return best
	}
	return metadata.Media{
		Artist: modeStr(func(m metadata.Media) string { return m.Artist }),
		Album:  modeStr(func(m metadata.Media) string { return m.Album }),
		Title:  modeStr(func(m metadata.Media) string { return m.Title }),
		Year:   modeInt(func(m metadata.Media) int { return m.Year }),
		Track:  modeInt(func(m metadata.Media) int { return m.Track }),
	}
}

// importRootPath decides where an imported directory tree is rooted in the
// virtual tree, in precedence order:
//
//  1. an explicit --dest path, if given;
//  2. a per-dir-type template from Config.ImportDest, rendered from the tree's
//     aggregated metadata (falls back to 3 if a template variable is empty);
//  3. the directory's absolute on-disk path (the default), which keeps imports
//     that share a basename from colliding under one root.
func (tie *TieClient) importRootPath(dir, dest, dirType string, meta []metadata.Media) (string, error) {
	if dest != "" {
		return FileURIScheme + "/" + strings.TrimPrefix(filepath.ToSlash(dest), "/"), nil
	}
	// A non-empty template for this dir-type reshapes the root from metadata; an
	// empty template value declares the dir-type as label-only (root at the
	// source path, no reshaping).
	if tmpl, ok := tie.Config.ImportDest[dirType]; ok && tmpl != "" {
		rendered, err := renderDestTemplate(tmpl, aggregateMetadata(meta))
		if err == nil {
			return FileURIScheme + "/" + strings.TrimPrefix(filepath.ToSlash(rendered), "/"), nil
		}
		fmt.Println("import destination template not applied, using source path:", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return FileURIScheme + filepath.ToSlash(absDir), nil
}

// ImportDir uploads a directory tree to the filehost and mirrors its on-disk
// hierarchy as nested virtual directories. The tree's root is chosen by
// importRootPath (explicit dest, a per-dir-type metadata template, or the
// source's absolute path). Every file is tagged by its content hash and linked
// to its real containing directory's DirUID, so the tree can be browsed by path;
// each file's own media type is detected individually. dirType is the tie-type
// label written on the import root (e.g. "audio-dir" for an album, or any custom
// type); it is a free-form string, so callers can pass built-in or user-defined
// dir-types. tags are applied to every file.
//
// Member order is left implicit: filesystem order already matches the tiedir
// manifest order. An explicit position triple is only written when a user later
// reorders members (future work).
// albumMeta aggregates album-level tags across the audio files of one imported
// directory so the directory node can carry a clean album title, artist, and
// year instead of only its folder name (which often embeds "Artist - Album").
// album stays empty when the files carry different album tags; a mixed artist
// collapses to "Various Artists".
type albumMeta struct {
	album, artist       string
	albumSet, artistSet bool
	albumMixed          bool
	artistMixed         bool
	year                int
}

func (a *albumMeta) add(m metadata.Media) {
	if m.Album != "" {
		if !a.albumSet {
			a.album, a.albumSet = m.Album, true
		} else if a.album != m.Album {
			a.albumMixed = true
		}
	}
	if m.Artist != "" {
		if !a.artistSet {
			a.artist, a.artistSet = m.Artist, true
		} else if a.artist != m.Artist {
			a.artistMixed = true
		}
	}
	if a.year == 0 && m.Year != 0 {
		a.year = m.Year
	}
}

// writeOps adds the resolved album/artist/year triples to a directory node,
// using Set so a re-import replaces rather than accumulates.
func (a *albumMeta) writeOps(batch *api.Batch, uid DirUID) {
	if a.albumSet && !a.albumMixed {
		batch.Set(str(uid), str(TieAlbum), []string{a.album})
	}
	if a.artistSet {
		artist := a.artist
		if a.artistMixed {
			artist = "Various Artists"
		}
		batch.Set(str(uid), str(TieArtist), []string{artist})
	}
	if a.year != 0 {
		batch.Set(str(uid), str(TieYear), []string{strconv.Itoa(a.year)})
	}
}

func (tie *TieClient) ImportDir(dir string, host FileHost, collection string, dirType string, tags []string, dest string, forcedArchive TieType) error {
	status := putlib.Upload(host.URL, dir, putlib.PutConfig{Client: HTTPClientFor(host)})
	if status.ErrorMsg != "" {
		return fmt.Errorf("Error uploading: %v\n%v\n", status.LastItem.Filename, status.ErrorMsg)
	}

	// Extract embedded metadata from each local file once, keyed by local path,
	// so it feeds both the root-path template (aggregated) and per-file tagging.
	fileMeta := make(map[string]metadata.Media)
	allMeta := make([]metadata.Media, 0, len(status.UploadedItems))
	// dirAlbums aggregates album-level metadata per directory, keyed by the
	// directory's path relative to the import root (the same key dirUID uses),
	// so each imported dir node can carry its album title/artist/year.
	dirAlbums := make(map[string]*albumMeta)
	for _, x := range status.UploadedItems {
		if x.MediaType == "inode/directory" {
			continue
		}
		m := ExtractMediaMetadata(x.Filename)
		fileMeta[x.Filename] = m
		allMeta = append(allMeta, m)
		if rel, err := filepath.Rel(dir, x.Filename); err == nil {
			key := filepath.Dir(rel)
			dm := dirAlbums[key]
			if dm == nil {
				dm = &albumMeta{}
				dirAlbums[key] = dm
			}
			dm.add(m)
		}
	}

	rootPath, err := tie.importRootPath(dir, dest, dirType, allMeta)
	if err != nil {
		return err
	}
	dirCache := make(map[string]DirUID)
	// want records, per directory DirUID, the set of content hashes this import
	// placed there. It is the source of truth for reconciliation: any existing file
	// child of a touched directory whose hash is not in this set is superseded
	// (content changed → new hash) or orphaned (removed/renamed on disk). Keying on
	// the hash (not the filename) is correct even when one hash has several names or
	// parents, since identical bytes collapse to a single content address.
	want := make(map[DirUID]map[string]bool)
	// dirUID resolves (creating on demand, with ancestors) the virtual DirUID for
	// a directory given by its path relative to the import root ("." = the root).
	dirUID := func(relDir string) (DirUID, error) {
		if uid, ok := dirCache[relDir]; ok {
			return uid, nil
		}
		vpath := rootPath
		if relDir != "." {
			vpath += "/" + filepath.ToSlash(relDir)
		}
		uid, err := tie.MkTieDirAll(vpath)
		if err != nil {
			return "", err
		}
		dirCache[relDir] = uid
		return uid, nil
	}

	// Tagging is the dominant request count on a large import: one Batch per file
	// would be thousands of tiny sequential POSTs. Accumulate the per-item tag ops
	// into one batch and flush in chunks, collapsing them into a handful of
	// requests. flushThreshold bounds the in-flight batch size (memory + request
	// size); api.Batch applies every op under a single Sync (api/batch.go).
	const flushThreshold = 1000
	batch := tie.NewBatchIn(collection)
	flush := func() error {
		if len(batch.Ops) == 0 {
			return nil
		}
		if _, err := tie.Batch(batch); err != nil {
			return err
		}
		batch = tie.NewBatchIn(collection)
		return nil
	}

	for _, x := range status.UploadedItems {
		rel, err := filepath.Rel(dir, x.Filename)
		if err != nil {
			return err
		}
		if x.MediaType == "inode/directory" {
			// Ensure the directory (and its ancestors) exist as DirUID entities,
			// so even childless directories are browsable.
			uid, err := dirUID(rel)
			if err != nil {
				return err
			}
			// A directory's tags, name, and size live on its DirUID (the stable
			// path-tree node), NOT on the immutable tiedir snapshot blob. This is
			// the single canonical identity for a directory in tag/type queries,
			// matching the .tags/.type control files, which also write the DirUID.
			// Every directory in the tree (and every file below) carries the given
			// tags, so "query/<tag> type:directory" and "query/<tag> type:file"
			// filter the same tagged set down to dirs or files respectively.
			// Use filepath.Base to get just the directory name, not the full path.
			dirname := filepath.Base(rel)
			if rel == "." {
				// Root directory of the import; use the last segment of the virtual
				// path for the name.
				dirname = filepath.Base(strings.TrimPrefix(rootPath, FileURIScheme))
			}
			appendTagDirOps(batch, uid, dirname, x.Size, tags)
			// Give the dir node a clean album title/artist/year aggregated from
			// its audio children, so browsers need not scrape them off tracks.
			if dm := dirAlbums[rel]; dm != nil {
				dm.writeOps(batch, uid)
			}
			// Link the DirUID to its immutable tiedir snapshot blob so the
			// content-addressed snapshot remains reachable from the live node.
			batch.Add(string(uid), str(TieTiedirHash), x.Hash)
			if len(batch.Ops) >= flushThreshold {
				if err := flush(); err != nil {
					return err
				}
			}
			continue
		}
		parent, err := dirUID(filepath.Dir(rel))
		if err != nil {
			return err
		}
		fileType, err := GetTieTypeFromPath(x.Filename)
		if err != nil {
			return err
		}
		fileType = applyArchiveOverride(fileType, forcedArchive)
		info := TagInfo{
			Hash:      x.Hash,
			File:      x.Filename,
			Size:      x.Size,
			MediaType: x.MediaType,
			Directory: parent,
			TieType:   fileType,
			Tags:      tags,
			Metadata:  fileMeta[x.Filename],
		}
		fmt.Println("tagging", info.Hash)
		appendTagOps(batch, info)
		if len(batch.Ops) >= flushThreshold {
			if err := flush(); err != nil {
				return err
			}
		}
		if want[parent] == nil {
			want[parent] = make(map[string]bool)
		}
		want[parent][x.Hash] = true
	}

	// Persist all buffered tag ops before reconciliation: reconcileDir queries the
	// committed children of each touched directory, so nothing may stay buffered.
	if err := flush(); err != nil {
		return err
	}

	rootUID, err := dirUID(".")
	if err != nil {
		return err
	}

	// Reconcile every directory this import touched: move superseded/orphaned file
	// children into per-file "_prev" history dirs (or delete them when history is
	// disabled), honoring the configured retention count. A directory that had no
	// files (only subdirs) is still reconciled with an empty want set so a file
	// deleted down to zero is handled.
	for relDir := range dirCache {
		uid := dirCache[relDir]
		if err := reconcileDir(tie, collection, uid, want[uid], tie.Config.PrevVersions); err != nil {
			return err
		}
	}

	return tie.SetDirType(rootUID, dirType)
}

// childEntry is one file child of a directory during reconciliation: its content
// hash, recorded display filename, and last-import date (for retention ordering).
type childEntry struct {
	Hash     string
	Filename string
	TagDate  time.Time
}

// supersededChildren returns the file children whose content hash this import did
// not place in the directory: a changed file uploads new bytes (a new hash, so the
// old hash is absent from want), and a removed/renamed file leaves its hash absent
// too. want is the set of content hashes this import placed in the directory.
//
// Reconciliation keys on the content hash, not the filename, because a hash can
// carry several filenames (identical bytes collapse to one content address) and be
// parented under several directories — the only unambiguous question is "did this
// import place this content here?".
func supersededChildren(children []childEntry, want map[string]bool) []childEntry {
	var out []childEntry
	for _, c := range children {
		if !want[c.Hash] {
			out = append(out, c)
		}
	}
	return out
}

// retentionDrops returns the oldest versions to remove so that at most keep
// remain, ordered by TagDate then Hash (ascending = oldest first). It returns
// nil when keep <= 0 (that case is handled by the no-history path) or when the
// count is already within the cap.
func retentionDrops(versions []childEntry, keep int) []childEntry {
	if keep <= 0 || len(versions) <= keep {
		return nil
	}
	sorted := make([]childEntry, len(versions))
	copy(sorted, versions)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].TagDate.Equal(sorted[j].TagDate) {
			return sorted[i].TagDate.Before(sorted[j].TagDate)
		}
		return sorted[i].Hash < sorted[j].Hash
	})
	return sorted[:len(sorted)-keep]
}

// fileChildren reads the file children of a directory DirUID via a reverse
// parent-edge lookup. Unlike ReadTieDir it does NOT filter by media tie-type, so
// plain "file"/"unknown-file" children are included (they must be, or a
// superseded .txt would be invisible to reconciliation). Directory-marked
// children (subdirs) and children with no recorded filename are skipped.
func fileChildren(tie *TieClient, uid DirUID) ([]childEntry, error) {
	rows, _, err := tie.Query(QuerySpec{Terms: []string{string(uid)}, Reverse: true, Filter: str(TieParent), Expand: true})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []childEntry
	for _, row := range rows {
		if RowHas(row, str(TieTypeProperty), str(TieDirectory)) {
			continue // subdir, not a file
		}
		// A hash can carry several filenames (identical bytes shared under
		// different names). Pick one deterministically for display and the
		// version location key; reconciliation itself keys on the hash.
		names := RowValues(row, str(TieFilename))
		if len(names) == 0 {
			continue // cannot reconcile a child with no recorded name
		}
		sort.Strings(names)
		out = append(out, childEntry{
			Hash:     row.Key,
			Filename: names[0],
			TagDate:  parseTagDate(RowFirst(row, str(TieTagDate))),
		})
	}
	return out, nil
}

// parentCount returns how many (hash,"parent",*) edges hash currently has.
func parentCount(tie *TieClient, hash string) (int, error) {
	row, err := tie.Get(hash)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return len(RowValues(row, str(TieParent))), nil
}

// detachChild removes the (hash,"parent",fromDir) edge. If that was the hash's
// last parent, its per-hash metadata (filename/name/tie-type/... and the tags it
// carries) is also deleted, since the content is no longer reachable from any
// directory. Shared content (still parented elsewhere) keeps its metadata.
func detachChild(tie *TieClient, collection string, hash string, fromDir DirUID) error {
	b := tie.NewBatchIn(collection)
	b.Delete(hash, str(TieParent), str(fromDir))
	if _, err := tie.Batch(b); err != nil {
		return err
	}
	if err := tie.Sync(); err != nil {
		return err
	}
	n, err := parentCount(tie, hash)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil // still reachable from another directory; keep shared metadata
	}
	row, err := tie.Get(hash)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	meta := tie.NewBatchIn(collection)
	for relation, values := range row.Attributes {
		for _, value2 := range values {
			meta.Delete(hash, relation, value2)
		}
	}
	if _, err := tie.Batch(meta); err != nil {
		return err
	}
	return tie.Sync()
}

// reconcileDir makes dir's file children match what this import placed there
// (want: the set of content hashes placed in this directory). Superseded/orphaned
// children are recorded in the isolated "<Collection>_prev" history collection,
// keeping at most prevVersions of each (oldest dropped first); when prevVersions
// is 0 the old edge is removed outright and the content is garbage-collected if
// no directory still references it. Diagnostics go to stderr so stdout stays clean.
func reconcileDir(tie *TieClient, collection string, uid DirUID, want map[string]bool, prevVersions int) error {
	children, err := fileChildren(tie, uid)
	if err != nil {
		return err
	}
	superseded := supersededChildren(children, want)
	if len(superseded) == 0 {
		return nil
	}

	for _, c := range superseded {
		fmt.Fprintf(os.Stderr, "reconcile: versioning superseded %s (%s) from %s\n", c.Filename, c.Hash, uid)
		if err := supersedeToPrev(tie, collection, uid, c.Filename, c.Hash); err != nil {
			return err
		}
	}
	return nil
}

func Tag(tie *TieClient, info TagInfo, collection string) error {
	fmt.Println("tagging", info.Hash)
	batch := tie.NewBatchIn(collection)
	appendTagOps(batch, info)
	if _, err := tie.Batch(batch); err != nil {
		return err
	}

	return nil
}

// appendTagOps appends the write ops that tag a single item (file) onto batch,
// without sending anything. Callers that tag many items reuse one batch to cut
// round-trips (see ImportDir); Tag is the single-item wrapper.
func appendTagOps(batch *api.Batch, info TagInfo) {
	filename := filepath.Base(info.File)
	hash := info.Hash
	name := strings.TrimRight(filename, filepath.Ext(filename)) // Remove extension
	batch.Add(hash, str(TieFilename), filename)
	batch.Add(hash, str(TieName), name)
	batch.Add(hash, str(TieMediaType), info.MediaType)
	batch.Add(hash, str(TieTypeProperty), str(info.TieType))
	batch.Add(hash, str(TieFilesize), strconv.Itoa(info.Size))
	// tag-date is single-valued (the last import time). Re-tagging the same hash
	// must not accumulate dates, so replace the whole relation in one op.
	batch.Set(hash, str(TieTagDate), []string{time.Now().Format(tagDateFormat)})
	for _, tag := range info.Tags {
		if len(tag) > 0 {
			if tag[0] == '-' {
				batch.Delete(hash, str(TieTag), tag[1:])
			} else {
				batch.Add(hash, str(TieTag), tag)
				batch.Add(str(TieTags), str(TieAll), tag)
			}
		}
	}
	if info.Metadata.Title != "" {
		batch.Add(hash, str(TieTitle), info.Metadata.Title)
	}
	if info.Metadata.Artist != "" {
		batch.Add(hash, str(TieArtist), info.Metadata.Artist)
	}
	if info.Metadata.Album != "" {
		batch.Add(hash, str(TieAlbum), info.Metadata.Album)
	}
	if info.Metadata.Year != 0 {
		batch.Add(hash, str(TieYear), strconv.Itoa(info.Metadata.Year))
	}
	if info.Metadata.Track != 0 {
		batch.Add(hash, str(TieTrack), strconv.Itoa(info.Metadata.Track))
	}
	// Audio playing time in seconds. Written under a raw "duration" property so
	// no stringer-backed TieProperty enum value has to be regenerated.
	if info.Metadata.Duration > 0 {
		batch.Add(hash, "duration", strconv.FormatFloat(info.Metadata.Duration, 'f', 3, 64))
	}
	if info.IsDir {
		batch.Add(hash, str(TieTypeProperty), str(TieDirectory))
	} else {
		batch.Add(hash, str(TieTypeProperty), str(TieFile))
	}
	if info.Directory != "" {
		batch.Add(hash, str(TieParent), str(info.Directory))
	}
}

// tagDir writes a directory's queryable metadata onto its DirUID: display name,
// size, tag date, and its tags (each tag also registered in the (tags,"all",<tag>)
// table, mirroring Tag/SetTags). A leading "-" on a tag deletes it. The structural
// (uid,"tie-type","directory") marker and the (uid,"path",...) triple are already
// written by MkTieDir, so they are not repeated here. Unlike Tag, this does not
// touch the tiedir content hash — a directory's identity for tagging is its DirUID.
func tagDir(tie *TieClient, uid DirUID, dirname string, size int, tags []string, collection string) error {
	batch := tie.NewBatchIn(collection)
	appendTagDirOps(batch, uid, dirname, size, tags)
	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return nil
}

// appendTagDirOps appends a directory's queryable metadata ops onto batch
// without sending. See tagDir for the semantics; ImportDir reuses one batch
// across many directories to cut round-trips.
func appendTagDirOps(batch *api.Batch, uid DirUID, dirname string, size int, tags []string) {
	batch.Add(str(uid), str(TieFilename), dirname)
	batch.Add(str(uid), str(TieName), dirname)
	batch.Add(str(uid), str(TieFilesize), strconv.Itoa(size))
	// tag-date is single-valued; replace the whole relation so re-import does not
	// accumulate dates on the DirUID.
	batch.Set(str(uid), str(TieTagDate), []string{time.Now().Format(tagDateFormat)})
	for _, tag := range tags {
		if len(tag) > 0 {
			if tag[0] == '-' {
				batch.Delete(str(uid), str(TieTag), tag[1:])
			} else {
				batch.Add(str(uid), str(TieTag), tag)
				batch.Add(str(TieTags), str(TieAll), tag)
			}
		}
	}
}

func (tie *TieClient) DirUIDFromPath(path string) (DirUID, error) {
	if !strings.HasPrefix(path, FileURIScheme) {
		path = FileURIScheme + path
	}
	rows, _, err := tie.Query(QuerySpec{Terms: []string{path}, Reverse: true, Filter: str(TiePath)})
	if errors.Is(err, ErrNotFound) {
		return "", nil // path not tied to any UID yet
	}
	if err != nil {
		return "", errors.New("error:'" + err.Error() + "'")
	}
	if len(rows) > 1 {
		// Data inconsistency: more than one UID claims this path (can happen
		// after a botched migration or a repeated import that created extra
		// directory nodes).  Warn on stderr and return the first entry so that
		// callers can continue rather than hard-failing.  Use the dedup script
		// (scripts/dedup-tie-paths.py) to clean the DB offline.
		fmt.Fprintf(os.Stderr,
			"warning: %d UIDs found for path %q; expected 1 — using %s (run dedup script to fix)\n",
			len(rows), path, rows[0].Key)
	}
	var uid DirUID
	for _, row := range rows {
		uid = DirUID(row.Key)
		break
	}

	return uid, nil
}

func (tie *TieClient) CreateTieRootDir() error {
	rootpath := FileURIScheme + "/"
	uid, err := tie.DirUIDFromPath(rootpath)
	if err != nil {
		return err
	}
	if uid != "" {
		return errors.New("Root dir already exists, with UID: " + uid.String())
	}

	uid = tie.newDirUID()

	b := tie.NewBatch()
	b.Add(str(uid), str(TieParent), str(uid))
	b.Add(str(uid), str(TiePath), rootpath)
	b.Add(str(uid), str(TieTypeProperty), str(TieDirectory))
	if _, err := tie.Batch(b); err != nil {
		return err
	}

	return nil
}

type Directory struct {
	Paths      []string
	Uid        DirUID
	SubDirs    []SubDirectory
	Files      []File
	Archives   []ArchiveEntry
	ParentUIDs []DirUID
}

type SubDirectory struct {
	Paths    []string
	Uid      DirUID
	DirTypes []TieType
}

type File struct {
	Filename  string
	Uid       string
	TieType   TieType
	MediaType string
	Size      int
	// TagDate is the file's last import time, parsed from its (hash,"tag-date")
	// triple. Zero when no date is recorded. The FUSE mount surfaces it as the
	// file's mtime.
	TagDate time.Time
}

// ArchiveEntry is a directory child that is a single archive blob (a zip)
// carrying an archive tie-type. Unlike a SubDirectory it has no graph
// child-edges: its "directory" contents live inside the blob and are produced
// by expanding it at read time (see io/archivelib). Uid is the blob's content
// hash, which is the expansion key — never pass it to ReadTieDir.
type ArchiveEntry struct {
	Filename string
	Hash     string
	TieType  TieType
	Size     int
	TagDate  time.Time
}

// archiveTypes are the tie-types that mark a blob as a browsable archive, most
// specific first.
var archiveTypes = []TieType{TieImageArchive, TieAudioArchive, TieVideoArchive, TieDocumentArchive, TieArchiveFile}

// IsArchiveType reports whether t marks a blob that should be expanded as a
// virtual directory of members.
func IsArchiveType(t TieType) bool {
	return slices.Contains(archiveTypes, t)
}

func containsArchiveType(types []string) bool {
	for _, t := range archiveTypes {
		if slices.Contains(types, str(t)) {
			return true
		}
	}
	return false
}

func archiveTypeOf(types []string) TieType {
	for _, t := range archiveTypes {
		if slices.Contains(types, str(t)) {
			return t
		}
	}
	return TieArchiveFile
}

func ReadTieDir(tie *TieClient, uid DirUID) (Directory, error) {
	var dir Directory
	self, err := tie.Get(string(uid))
	if err != nil {
		return dir, errors.New("error:'" + err.Error() + "'")
	}
	dir.Uid = uid
	dir.Paths = RowValues(self, str(TiePath))
	parents := RowValues(self, str(TieParent))
	dir.ParentUIDs = make([]DirUID, 0, len(parents))
	for _, x := range parents {
		dir.ParentUIDs = append(dir.ParentUIDs, DirUID(x))
	}

	rows, _, err := tie.Query(QuerySpec{Terms: []string{string(uid)}, Reverse: true, Expand: true})
	if errors.Is(err, ErrNotFound) {
		return dir, nil // directory has no children
	}
	if err != nil {
		return dir, errors.New("error:'" + err.Error() + "'")
	}
	for _, row := range rows {
		types := row.Attributes[str(TieTypeProperty)]
		switch true {
		case slices.Contains(types, str(TieDirectory)):
			subDir := SubDirectory{
				Uid:      DirUID(row.Key),
				Paths:    RowValues(row, str(TiePath)),
				DirTypes: SliceToTieType(types),
			}
			dir.SubDirs = append(dir.SubDirs, subDir)
		case containsArchiveType(types):
			size, _ := strconv.Atoi(RowFirst(row, str(TieFilesize)))
			filename := RowFirst(row, str(TieFilename))
			if filename == "" {
				filename = row.Key
			}
			dir.Archives = append(dir.Archives, ArchiveEntry{
				Filename: filename,
				Hash:     row.Key,
				TieType:  archiveTypeOf(types),
				Size:     size,
				TagDate:  parseTagDate(RowFirst(row, str(TieTagDate))),
			})
		default:
			// Any child that is neither a subdirectory nor a browsable archive is
			// a plain file. This covers every media type (image/video/audio/
			// document) and, crucially, structural "file"/"unknown-file" children
			// (e.g. a copied-in .txt): without this case they were stored but
			// invisible in every listing. Since children reach a DirUID only via a
			// parent edge (dir/archive/file), a default match cannot pick up a
			// non-child row.
			size, _ := strconv.Atoi(RowFirst(row, str(TieFilesize)))
			filename := RowFirst(row, str(TieFilename))
			if filename == "" {
				filename = row.Key // fall back to the hash when no filename is recorded
			}
			f := File{
				Uid:       row.Key,
				Filename:  filename,
				TieType:   StringToTieType(strings.Join(types, ", ")),
				MediaType: RowFirst(row, str(TieMediaType)),
				Size:      size,
				TagDate:   parseTagDate(RowFirst(row, str(TieTagDate))),
			}
			dir.Files = append(dir.Files, f)
		}
	}

	// dir.Files comes from a map iteration, so its order is nondeterministic.
	// Sort it stably (filename, then hash) so callers that disambiguate colliding
	// names produce the same result on every readdir and matching lookup.
	sort.Slice(dir.Files, func(i, j int) bool {
		if dir.Files[i].Filename != dir.Files[j].Filename {
			return dir.Files[i].Filename < dir.Files[j].Filename
		}
		return dir.Files[i].Uid < dir.Files[j].Uid
	})
	sort.Slice(dir.Archives, func(i, j int) bool {
		if dir.Archives[i].Filename != dir.Archives[j].Filename {
			return dir.Archives[i].Filename < dir.Archives[j].Filename
		}
		return dir.Archives[i].Hash < dir.Archives[j].Hash
	})

	return dir, nil
}

// parseTagDate parses a stored tag-date value. Current values carry microsecond
// precision (tagDateFormat); values written before that used second resolution
// (time.DateTime). Both are accepted. A blank or malformed value yields the zero
// Time.
func parseTagDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.ParseInLocation(tagDateFormat, s, time.Local); err == nil {
		return t
	}
	if t, err := time.ParseInLocation(time.DateTime, s, time.Local); err == nil {
		return t
	}
	return time.Time{}
}

// RenameFile renames and/or moves a file in the path tree. The file's identity
// is its content hash, so the display name lives in the (hash,"filename") and
// (hash,"name") triples; renaming updates both. A move swaps the (hash,"parent")
// edge from oldParent to newParent.
//
// Because the name is a property of the content hash, renaming a file changes
// its name everywhere the same content appears (other directories, every tag
// query). This is inherent to the content-addressed model.
func RenameFile(tie *TieClient, hash string, oldParent, newParent DirUID, newName string) error {
	row, err := tie.Get(hash)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	oldFilename := RowFirst(row, str(TieFilename))
	oldName := RowFirst(row, str(TieName))

	batch := tie.NewBatch()
	if newName != oldFilename {
		newBase := strings.TrimSuffix(newName, filepath.Ext(newName))
		// AddOnFailure so a file that never recorded a filename (the
		// hash-fallback case in ReadTieDir) still gets one.
		fn := tie.NewUpdate(hash, str(TieFilename), oldFilename, newName)
		fn.AddOnFailure = true
		batch.Update(fn)
		nm := tie.NewUpdate(hash, str(TieName), oldName, newBase)
		nm.AddOnFailure = true
		batch.Update(nm)
	}
	if newParent != oldParent {
		// parent is potentially multi-valued (same content in several dirs),
		// so delete the specific old edge rather than blanket-updating.
		batch.Delete(hash, str(TieParent), str(oldParent))
		batch.Add(hash, str(TieParent), str(newParent))
	}
	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return tie.Sync()
}

// RenameDir renames and/or moves a directory in the path tree. A directory's
// identity in the path tree is its (uid,"path") triple, and every descendant
// directory carries the renamed prefix in its own path, so the rename cascades
// over all descendant dirs. Files need no path rewrite: they have no path
// triple and their parent DirUID is unchanged. A move swaps the top dir's
// (uid,"parent") edge.
func RenameDir(tie *TieClient, uid DirUID, oldPath, newPath string, oldParent, newParent DirUID) error {
	descendants, err := collectDescendantDirs(tie, uid)
	if err != nil {
		return err
	}

	batch := tie.NewBatch()
	own := tie.NewUpdate(str(uid), str(TiePath), oldPath, newPath)
	own.AddOnFailure = true
	batch.Update(own)

	for _, d := range descendants {
		for _, p := range d.Paths {
			if !strings.HasPrefix(p, oldPath) {
				continue
			}
			np := newPath + strings.TrimPrefix(p, oldPath)
			du := tie.NewUpdate(str(d.Uid), str(TiePath), p, np)
			du.AddOnFailure = true
			batch.Update(du)
		}
	}

	if newParent != oldParent {
		batch.Delete(str(uid), str(TieParent), str(oldParent))
		batch.Add(str(uid), str(TieParent), str(newParent))
	}

	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return tie.Sync()
}

// collectDescendantDirs walks the path tree under root (breadth-first via
// ReadTieDir) and returns every descendant directory, excluding root itself.
func collectDescendantDirs(tie *TieClient, root DirUID) ([]SubDirectory, error) {
	var out []SubDirectory
	queue := []DirUID{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		dir, err := ReadTieDir(tie, cur)
		if err != nil {
			return nil, err
		}
		for _, sub := range dir.SubDirs {
			out = append(out, sub)
			queue = append(queue, sub.Uid)
		}
	}
	return out, nil
}

// GetTags returns the tags currently attached to a content hash, sorted. It
// reads the (hash,"tag",<tag>) triples. A hash with no tags returns an empty
// slice, not an error.
func GetTags(tie *TieClient, hash string) ([]string, error) {
	row, err := tie.Get(hash)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tags := RowValues(row, str(TieTag))
	sort.Strings(tags)
	return tags, nil
}

// SetTags replaces the tags on a content hash with newTags, computing the
// minimal diff against the current tags: tags in newTags but not stored are
// added (along with the (tags,"all",<tag>) registry entry that Tag writes),
// tags stored but absent from newTags are removed. Empty entries are ignored.
// The change is committed and synced.
//
// Because tags attach to the content hash, this affects the tags everywhere the
// same content appears and in every tag query view.
func SetTags(tie *TieClient, hash string, newTags []string) error {
	current, err := GetTags(tie, hash)
	if err != nil {
		return err
	}

	want := make(map[string]bool, len(newTags))
	for _, t := range newTags {
		if t = strings.TrimSpace(t); t != "" {
			want[t] = true
		}
	}
	have := make(map[string]bool, len(current))
	for _, t := range current {
		have[t] = true
	}

	batch := tie.NewBatch()
	changed := false
	for t := range want {
		if !have[t] {
			batch.Add(hash, str(TieTag), t)
			batch.Add(str(TieTags), str(TieAll), t)
			changed = true
		}
	}
	for t := range have {
		if !want[t] {
			batch.Delete(hash, str(TieTag), t)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return tie.Sync()
}

// TaggedFile is one entry in a tag-derived virtual directory: its content hash
// (used to fetch bytes, or a tiedir blob, from the filehost), display filename,
// size, and whether it is a directory. A tagged directory's hash points at an
// immutable tiedir blob, so it can be expanded with the content-addressed tree.
type TaggedFile struct {
	Hash     string
	Filename string
	Size     int
	IsDir    bool
	// IsArchive marks a single archive blob (a zip) that is browsable as a
	// virtual directory of its members. It is a file structurally (IsDir is
	// false), but consumers should expand it at read time via io/archivelib
	// rather than treating it as plain downloadable content. TieType carries the
	// specific archive type when IsArchive is set.
	IsArchive bool
	TieType   TieType
}

// ListTags returns tag names known to the store, read from the
// ("tags", "all", <tag>) registry that Tag writes, ordered by tag name.
// offset/limit paginate; limit <= 0 means no limit. The second return is the
// total number of tags before pagination.
func (tie *TieClient) ListTags(offset, limit int) ([]string, int, error) {
	rows, total, err := tie.Query(QuerySpec{
		Terms:  []string{str(TieTags)},
		Filter: str(TieAll),
		Offset: offset,
		Limit:  limit,
	})
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var tags []string
	for _, row := range rows {
		tags = append(tags, RowValues(row, str(TieAll))...)
	}
	return tags, total, nil
}

// tagBatchChunk bounds how many triple ops a single rename/delete batch carries.
// A tag applied across a large store can touch millions of subjects; splitting
// the rewrite into fixed-size batches keeps any one request bounded rather than
// building one enormous batch.
const tagBatchChunk = 1000

// taggedSubjects returns the keys of every subject (file hash or directory
// DirUID) that carries (subject,"tag",tag). It is a reverse lookup on the tag
// relation; no Expand, since only the subject keys are needed. A tag no item
// carries yields an empty slice, not an error.
func (tie *TieClient) taggedSubjects(tag string) ([]string, error) {
	rows, _, err := tie.Query(QuerySpec{
		Terms:   []string{tag},
		Filter:  str(TieTag),
		Reverse: true,
		Limit:   -1,
	})
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	subjects := make([]string, 0, len(rows))
	for _, row := range rows {
		subjects = append(subjects, row.Key)
	}
	return subjects, nil
}

// RegisterTag records tag in the ("tags","all",<tag>) registry that backs
// ListTags, so a tag name is known to the store even before any file carries
// it. It writes the same registry triple Tag/SetTags write. Registering an
// existing tag is a harmless no-op. An empty tag is rejected.
func (tie *TieClient) RegisterTag(tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return errors.New("RegisterTag: tag must not be empty")
	}
	if _, err := tie.Add(str(TieTags), str(TieAll), tag); err != nil {
		return err
	}
	return tie.Sync()
}

// DeleteTag removes tag from every item that carries it and from the
// ("tags","all",<tag>) registry, across the current collection. It returns the
// number of items the tag was removed from. The registry entry is dropped even
// when no item carries the tag, so a dangling registered tag can be cleaned up.
//
// Because a tag attaches to a content hash, this removes the tag everywhere that
// content appears and from every tag-query view.
func (tie *TieClient) DeleteTag(tag string) (int, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return 0, errors.New("DeleteTag: tag must not be empty")
	}
	subjects, err := tie.taggedSubjects(tag)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(subjects); i += tagBatchChunk {
		end := min(i+tagBatchChunk, len(subjects))
		b := tie.NewBatch()
		for _, subject := range subjects[i:end] {
			b.Delete(subject, str(TieTag), tag)
		}
		if _, err := tie.Batch(b); err != nil {
			return 0, err
		}
	}
	reg := tie.NewBatch()
	reg.Delete(str(TieTags), str(TieAll), tag)
	if _, err := tie.Batch(reg); err != nil {
		return 0, err
	}
	if err := tie.Sync(); err != nil {
		return 0, err
	}
	return len(subjects), nil
}

// RenameTag rewrites tag oldTag to newTag on every item that carries it and in
// the ("tags","all",<tag>) registry, across the current collection. It returns
// the number of items rewritten. The registry entry is renamed even when no
// item carries the tag, so a dangling registered tag can be renamed too.
//
// Because a tag attaches to a content hash, this renames the tag everywhere that
// content appears and in every tag-query view. Re-tagging an item that already
// carries newTag is a harmless no-op.
func (tie *TieClient) RenameTag(oldTag, newTag string) (int, error) {
	oldTag = strings.TrimSpace(oldTag)
	newTag = strings.TrimSpace(newTag)
	if oldTag == "" || newTag == "" {
		return 0, errors.New("RenameTag: tags must not be empty")
	}
	if oldTag == newTag {
		return 0, nil
	}
	subjects, err := tie.taggedSubjects(oldTag)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(subjects); i += tagBatchChunk {
		end := min(i+tagBatchChunk, len(subjects))
		b := tie.NewBatch()
		for _, subject := range subjects[i:end] {
			// A batch runs deletes before adds; oldTag and newTag differ, so
			// there is no self-conflict on a subject.
			b.Delete(subject, str(TieTag), oldTag)
			b.Add(subject, str(TieTag), newTag)
		}
		if _, err := tie.Batch(b); err != nil {
			return 0, err
		}
	}
	reg := tie.NewBatch()
	reg.Delete(str(TieTags), str(TieAll), oldTag)
	reg.Add(str(TieTags), str(TieAll), newTag)
	if _, err := tie.Batch(reg); err != nil {
		return 0, err
	}
	if err := tie.Sync(); err != nil {
		return 0, err
	}
	return len(subjects), nil
}

// FilesWithTags returns the files that carry ALL of include and NONE of exclude,
// optionally scoped to a single tie-type value (e.g. "audio-file" for "find music
// with tag1, tag2 but not tag4", or a custom dir-type label like "live-album").
// Tags share the "tag" relation so they AND/NOT together inside one QueryTags
// call; the tie-type scoping keys on a different relation (tie-type), so it rides
// along as the query's Scope, which the server intersects by hash identity — no
// client-side filtering. Pass an empty scope to query across all types. When
// include is empty the whole scope is browsed directly (or, with no scope,
// nothing is returned since there is no set to seed from). The scope is a raw
// tie-type value string so built-in media types and custom labels are handled
// uniformly. The server paginates via offset/limit; limit <= 0 means no limit.
// The second return is the total number of matching files before pagination.
func (tie *TieClient) FilesWithTags(scope string, include, exclude []string, offset, limit int) ([]TaggedFile, int, error) {
	// With no tags to seed from, a query needs a scope to browse.
	if len(include) == 0 {
		if scope == "" {
			return nil, 0, nil
		}
		return tie.filesOfType(scope, offset, limit)
	}

	rows, total, err := tie.Query(QuerySpec{
		Terms:   include,
		Exclude: exclude,
		Scope:   scope,
		Filter:  str(TieTag),
		Reverse: true,
		Expand:  true,
		Offset:  offset,
		Limit:   limit,
	})
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}

	files := make([]TaggedFile, 0, len(rows))
	for _, row := range rows {
		files = append(files, taggedFileFrom(row))
	}
	return files, total, nil
}

// filesOfType builds TaggedFiles for every hash carrying the tie-type value
// scope, pulling per-hash metadata via an expanded reverse query. The server
// paginates via offset/limit.
func (tie *TieClient) filesOfType(scope string, offset, limit int) ([]TaggedFile, int, error) {
	rows, total, err := tie.Query(QuerySpec{
		Terms:   []string{scope},
		Filter:  str(TieTypeProperty),
		Reverse: true,
		Expand:  true,
		Offset:  offset,
		Limit:   limit,
	})
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	files := make([]TaggedFile, 0, len(rows))
	for _, row := range rows {
		files = append(files, taggedFileFrom(row))
	}
	return files, total, nil
}

// UntaggedFiles returns items that carry a tie-type but no tag, i.e. files/dirs
// in the store that have never been tagged. scope names a single tie-type to
// browse (e.g. "audio-file", or a custom dir-type label); an empty scope browses
// the structural universes "file" and "directory", covering every imported file
// and directory. offset/limit paginate the untagged result; limit <= 0 means no
// limit. The second return is the total number of untagged items before
// pagination.
//
// The "has no tag" filter runs server-side via QuerySpec.MissingRelation, so
// only untagged rows cross the wire (not the scope's full metadata). A single
// scope is paginated by the server; the empty-scope case unions the two
// structural types and paginates the merged result client-side.
func (tie *TieClient) UntaggedFiles(scope string, offset, limit int) ([]TaggedFile, int, error) {
	// A single explicit scope paginates entirely server-side.
	if scope != "" {
		return tie.untaggedOfType(scope, offset, limit)
	}

	// Empty scope: union the structural "file" and "directory" universes. Each
	// side is already tag-filtered by the server; merge, sort, then paginate.
	var untagged []TaggedFile
	seen := make(map[string]bool)
	for _, s := range []string{str(TieFile), str(TieDirectory)} {
		files, _, err := tie.untaggedOfType(s, 0, -1)
		if err != nil {
			return nil, 0, err
		}
		for _, f := range files {
			if seen[f.Hash] {
				continue // same subject under both structural scopes
			}
			seen[f.Hash] = true
			untagged = append(untagged, f)
		}
	}

	// Stable order (filename, then hash) so client-side pagination is
	// deterministic across calls, matching ReadTieDir's ordering.
	sort.Slice(untagged, func(i, j int) bool {
		if untagged[i].Filename != untagged[j].Filename {
			return untagged[i].Filename < untagged[j].Filename
		}
		return untagged[i].Hash < untagged[j].Hash
	})

	total := len(untagged)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	untagged = untagged[offset:]
	if limit > 0 && limit < len(untagged) {
		untagged = untagged[:limit]
	}
	return untagged, total, nil
}

// untaggedOfType returns the untagged items of one tie-type via a reverse
// tie-type query with the server-side MissingRelation="tag" predicate, so the
// daemon returns only rows lacking a tag. The server paginates via offset/limit.
func (tie *TieClient) untaggedOfType(scope string, offset, limit int) ([]TaggedFile, int, error) {
	rows, total, err := tie.Query(QuerySpec{
		Terms:           []string{scope},
		Filter:          str(TieTypeProperty),
		MissingRelation: str(TieTag),
		Reverse:         true,
		Expand:          true,
		Offset:          offset,
		Limit:           limit,
	})
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	files := make([]TaggedFile, 0, len(rows))
	for _, row := range rows {
		files = append(files, taggedFileFrom(row))
	}
	return files, total, nil
}

// taggedFileFrom builds a TaggedFile from a hash and its next-level metadata,
// falling back to the hash as the display name when no filename is recorded.
// For directories, tries TieFilename first, then TiePath (for DirUIDs), extracting
// the basename from either.
func taggedFileFrom(row Row) TaggedFile {
	isDir := RowHas(row, str(TieTypeProperty), str(TieDirectory))
	types := row.Attributes[str(TieTypeProperty)]
	isArchive := !isDir && containsArchiveType(types)
	filename := RowFirst(row, str(TieFilename))

	// If filename contains a path separator, extract just the basename.
	// This handles both old imports (full paths) and ensures consistency.
	if filename != "" && strings.ContainsAny(filename, "/\\") {
		filename = filepath.Base(filename)
	}

	if filename == "" && isDir {
		// Directories (especially DirUIDs) might use TiePath; extract the basename.
		if paths := RowValues(row, str(TiePath)); len(paths) > 0 {
			// Use the first path's basename as the display name.
			p := paths[0]
			p = strings.TrimPrefix(p, FileURIScheme)
			p = strings.TrimRight(p, "/")
			if i := strings.LastIndex(p, "/"); i >= 0 {
				filename = p[i+1:]
			} else {
				filename = p
			}
		}
	}

	// Final fallback: use the hash/UID as the display name.
	if filename == "" {
		filename = row.Key
	}

	size, _ := strconv.Atoi(RowFirst(row, str(TieFilesize)))
	tf := TaggedFile{Hash: row.Key, Filename: filename, Size: size, IsDir: isDir, IsArchive: isArchive}
	if isArchive {
		tf.TieType = archiveTypeOf(types)
	}
	return tf
}

// MediaRelation is a named, directed association from one media item to another,
// both identified by content hash (e.g. {hashA, "sampled-from", hashB}).
type MediaRelation struct {
	FromHash string
	Relation string
	ToHash   string
}

// RelateFiles records an open-vocabulary relation between two media items by
// content hash, e.g. RelateFiles(track, "sampled-from", source). Both directed
// edges are written as forward triples — (from, relation, to) and
// (to, relation, from) — because forward associations are always stored (the
// reverse index is a fixed allowlist that arbitrary relation names are not in).
// Writing both directions makes the relation browsable from either media item
// with a plain forward lookup. relation must not be one of the reserved metadata
// relations (filename, tag, parent, ...).
func (tie *TieClient) RelateFiles(fromHash, relation, toHash string) error {
	if !metadata.IsHexHash(fromHash) || !metadata.IsHexHash(toHash) {
		return errors.New("RelateFiles: both endpoints must be content hashes")
	}
	if relation == "" {
		return errors.New("RelateFiles: relation must not be empty")
	}
	b := tie.NewBatch()
	b.Add(fromHash, relation, toHash)
	b.Add(toHash, relation, fromHash)
	if _, err := tie.Batch(b); err != nil {
		return err
	}
	return nil
}

// RelationsFrom returns every media-to-media relation recorded on hash. Because
// RelateFiles writes both directions as forward edges, this one forward lookup
// surfaces relations pointing both from and into hash. Only triples whose object
// is itself a content hash are returned, so file metadata (filename, tag, ...) is
// excluded.
func (tie *TieClient) RelationsFrom(hash string) ([]MediaRelation, error) {
	row, err := tie.Get(hash)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rels []MediaRelation
	for relation, values := range row.Attributes {
		for _, value2 := range values {
			if metadata.IsHexHash(value2) {
				rels = append(rels, MediaRelation{FromHash: hash, Relation: relation, ToHash: value2})
			}
		}
	}
	return rels, nil
}

// tieDirAncestors returns the virtual directory paths that make up p, from the
// top-level directory down to p itself, each prefixed with FileURIScheme. The
// root "tie:/" is not included (it is created separately). Virtual paths are
// always absolute, so a leading slash is assumed and the input is cleaned to
// collapse "." / ".." and redundant separators.
//
//	"tie:/a/b/c" -> ["tie:/a", "tie:/a/b", "tie:/a/b/c"]
//	"/a/b"        -> ["tie:/a", "tie:/a/b"]
//	"tie:/"      -> [] (root only)
func tieDirAncestors(p string) []string {
	p = strings.TrimPrefix(p, FileURIScheme)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	if p == "/" {
		return nil
	}
	segments := strings.Split(strings.TrimPrefix(p, "/"), "/")
	ancestors := make([]string, 0, len(segments))
	prefix := FileURIScheme
	for _, seg := range segments {
		prefix = prefix + "/" + seg
		ancestors = append(ancestors, prefix)
	}
	return ancestors
}

func (tie *TieClient) MkTieDirAll(dirPath string) (DirUID, error) {
	if err := tie.CreateTieRootDir(); err != nil &&
		!strings.HasPrefix(err.Error(), "Root dir already exists") {
		return "", err
	}

	var uid DirUID
	for _, ancestor := range tieDirAncestors(dirPath) {
		created, err := tie.MkTieDir(ancestor)
		if err != nil && !strings.HasSuffix(err.Error(), "Directory exists") {
			return "", err
		}
		uid = created
	}

	return uid, nil
}

func (tie *TieClient) MkTieDir(path string) (DirUID, error) {
	if !strings.HasPrefix(path, FileURIScheme) {
		path = FileURIScheme + path
	}
	uid, err := tie.DirUIDFromPath(path)
	if err != nil {
		return "", err
	}
	if uid != "" { // Dir already exists
		return uid, errors.New("Cannot create directory '" + path + "': Directory exists")
	}

	parentPath := filepath.Dir(path)
	if parentPath == FileURIScheme {
		parentPath = FileURIScheme + "/"
	}

	uid = tie.newDirUID()

	parentUID, err := tie.DirUIDFromPath(parentPath)
	if err != nil {
		return "", err
	}

	b := tie.NewBatch()
	b.Add(str(uid), str(TieParent), str(parentUID))
	b.Add(str(uid), str(TiePath), path)
	b.Add(str(uid), str(TieTypeProperty), str(TieDirectory))
	if _, err := tie.Batch(b); err != nil {
		return uid, err
	}

	return uid, nil
}

// typeRegistrySubject is the subject of the dir-type registry: a directory's
// classification labels are registered as (types,"all",<label>) so /query/types
// can list every custom label in use, mirroring the (tags,"all",<tag>) registry
// that Tag/SetTags write. The structural "directory" marker is never registered.
const typeRegistrySubject = "types"

func (tie *TieClient) SetDirType(uid DirUID, dirType string) error {
	if _, err := tie.Add(str(uid), str(TieTypeProperty), dirType); err != nil {
		return err
	}
	if dirType != str(TieDirectory) {
		if _, err := tie.Add(typeRegistrySubject, str(TieAll), dirType); err != nil {
			return err
		}
	}
	return nil
}

// GetDirType returns a directory's classification labels — its
// (uid,"tie-type",*) values with the structural "directory" marker filtered out
// — sorted. The marker is what ReadTieDir uses to tell subdirs from files, so it
// is never surfaced here. A directory with no extra labels returns an empty
// slice, not an error.
func GetDirType(tie *TieClient, uid DirUID) ([]string, error) {
	row, err := tie.Get(string(uid))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var labels []string
	for _, t := range RowValues(row, str(TieTypeProperty)) {
		if t != str(TieDirectory) {
			labels = append(labels, t)
		}
	}
	sort.Strings(labels)
	return labels, nil
}

// SetDirTypes replaces a directory's classification labels with newLabels,
// diffing against the current labels: added labels get (uid,"tie-type",<label>)
// plus the (types,"all",<label>) registry entry, removed labels get a Delete.
// The structural "directory" marker is preserved untouched (never added, never
// removed), so a text-editor rewrite of the .type file cannot break navigation.
// Empty entries are ignored. The change is committed and synced.
func SetDirTypes(tie *TieClient, uid DirUID, newLabels []string) error {
	current, err := GetDirType(tie, uid)
	if err != nil {
		return err
	}

	want := make(map[string]bool, len(newLabels))
	for _, t := range newLabels {
		if t = strings.TrimSpace(t); t != "" && t != str(TieDirectory) {
			want[t] = true
		}
	}
	have := make(map[string]bool, len(current))
	for _, t := range current {
		have[t] = true
	}

	batch := tie.NewBatch()
	changed := false
	for t := range want {
		if !have[t] {
			batch.Add(string(uid), str(TieTypeProperty), t)
			batch.Add(typeRegistrySubject, str(TieAll), t)
			changed = true
		}
	}
	for t := range have {
		if !want[t] {
			batch.Delete(string(uid), str(TieTypeProperty), t)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return tie.Sync()
}

// ListDirTypes returns the dir-type classification labels known to the store: the
// custom labels registered in (types,"all",<label>) unioned with the built-in
// directory TieTypes (the *-dir / *-archive / directory stringers), sorted and
// de-duplicated. The union means /query/types shows both user-created labels and
// the built-in vocabulary without pre-seeding the registry with built-ins.
func (tie *TieClient) ListDirTypes() ([]string, error) {
	set := make(map[string]bool)
	for t := TieImageDir; t <= TieDirectory; t++ {
		set[t.String()] = true
	}
	rows, _, err := tie.Query(QuerySpec{Terms: []string{typeRegistrySubject}, Filter: str(TieAll)})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	for _, row := range rows {
		for _, value2 := range RowValues(row, str(TieAll)) {
			set[value2] = true
		}
	}
	labels := make([]string, 0, len(set))
	for t := range set {
		labels = append(labels, t)
	}
	sort.Strings(labels)
	return labels, nil
}

// newDirUID mints a DirUID as a 64-char hex string of 32 random bytes. This
// matches the content-hash shape so metadata.HexHashBlobPolicy stores it as one
// flat record instead of chunking a longer string across the trie.
func (tie *TieClient) newDirUID() DirUID {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("client: reading random bytes for DirUID: " + err.Error())
	}
	return DirUID(hex.EncodeToString(b[:]))
}
