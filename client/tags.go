package client

import (
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/tie/api"
)

// Tag management and tag queries: reading and replacing the tags on a subject,
// the tag registry (register, rename, delete), and the file listings that walk
// tags — FilesWithTags, UntaggedFiles. Split out of tag.go (§7.11).

func Tag(tie *TieClient, info TagInfo) error {
	batch := tie.NewBatch()
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
	name := nameFromFilename(filename) // Remove extension
	batch.Add(hash, string(RelFilename), filename)
	batch.Add(hash, string(RelName), name)
	batch.Add(hash, string(RelMediaType), info.MediaType)
	batch.Add(hash, string(RelType), str(info.TieType))
	batch.Add(hash, string(RelFilesize), strconv.Itoa(info.Size))
	// tag-date is single-valued (the last import time). Re-tagging the same hash
	// must not accumulate dates, so replace the whole relation in one op.
	batch.Set(hash, string(RelTagDate), []string{time.Now().Format(tagDateFormat)})
	for _, tag := range info.Tags {
		if len(tag) > 0 {
			if tag[0] == '-' {
				batch.Delete(hash, string(RelTag), tag[1:])
			} else {
				batch.Add(hash, string(RelTag), tag)
				batch.Add(string(RelTags), string(RelAll), tag)
			}
		}
	}
	if info.Metadata.Title != "" {
		batch.Add(hash, string(RelTitle), info.Metadata.Title)
	}
	if info.Metadata.Artist != "" {
		batch.Add(hash, string(RelArtist), info.Metadata.Artist)
	}
	// Album artist (e.g. "Various Artists").
	if info.Metadata.AlbumArtist != "" {
		batch.Add(hash, string(RelAlbumArtist), info.Metadata.AlbumArtist)
	}
	if info.Metadata.Album != "" {
		batch.Add(hash, string(RelAlbum), info.Metadata.Album)
	}
	if info.Metadata.Year != 0 {
		batch.Add(hash, string(RelYear), strconv.Itoa(info.Metadata.Year))
	}
	if info.Metadata.Track != 0 {
		batch.Add(hash, string(RelTrack), strconv.Itoa(info.Metadata.Track))
	}
	// Media-vocabulary fields (media.go). Set, so a re-tag replaces values
	// instead of accumulating a stale disc number or genre spelling.
	if info.Metadata.TrackTotal != 0 {
		batch.Set(hash, string(RelTrackTotal), []string{strconv.Itoa(info.Metadata.TrackTotal)})
	}
	if info.Metadata.Disc != 0 {
		batch.Set(hash, string(RelDisc), []string{strconv.Itoa(info.Metadata.Disc)})
	}
	if info.Metadata.DiscTotal != 0 {
		batch.Set(hash, string(RelDiscTotal), []string{strconv.Itoa(info.Metadata.DiscTotal)})
	}
	if g := SplitGenres(info.Metadata.Genre); len(g) > 0 {
		batch.Set(hash, string(RelGenre), g)
	}
	if info.Metadata.Composer != "" {
		batch.Set(hash, string(RelComposer), []string{info.Metadata.Composer})
	}
	if info.TieType == TieAudioFile {
		batch.Set(hash, string(RelMetaVersion), []string{MetaVersion})
	}
	// Audio playing time in seconds.
	if info.Metadata.Duration > 0 {
		batch.Add(hash, string(RelDuration), strconv.FormatFloat(info.Metadata.Duration, 'f', 3, 64))
	}
	if info.IsDir {
		batch.Add(hash, string(RelType), str(TieDirectory))
	} else {
		batch.Add(hash, string(RelType), str(TieFile))
	}
	if info.Directory != "" {
		batch.Add(hash, string(RelParent), str(info.Directory))
	}
}

// appendTagDirOps appends a directory's queryable metadata ops onto batch
// without sending. The semantics are described on appendTagDirOps' callers;
// ImportDir reuses one batch across many directories to cut round-trips.
func appendTagDirOps(batch *api.Batch, uid DirUID, dirname string, size int, tags []string) {
	batch.Add(str(uid), string(RelFilename), dirname)
	batch.Add(str(uid), string(RelName), dirname)
	batch.Add(str(uid), string(RelFilesize), strconv.Itoa(size))
	// tag-date is single-valued; replace the whole relation so re-import does not
	// accumulate dates on the DirUID.
	batch.Set(str(uid), string(RelTagDate), []string{time.Now().Format(tagDateFormat)})
	for _, tag := range tags {
		if len(tag) > 0 {
			if tag[0] == '-' {
				batch.Delete(str(uid), string(RelTag), tag[1:])
			} else {
				batch.Add(str(uid), string(RelTag), tag)
				batch.Add(string(RelTags), string(RelAll), tag)
			}
		}
	}
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
	tags := RowValues(row, RelTag)
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
			batch.Add(hash, string(RelTag), t)
			batch.Add(string(RelTags), string(RelAll), t)
			changed = true
		}
	}
	for t := range have {
		if !want[t] {
			batch.Delete(hash, string(RelTag), t)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return nil
}

// ListTags returns tag names known to the store, read from the
// ("tags", "all", <tag>) registry that Tag writes, ordered by tag name.
// offset/limit paginate; limit <= 0 means no limit. The second return is the
// total number of tags before pagination.
func (tie *TieClient) ListTags(offset, limit int) ([]string, int, error) {
	rows, total, err := tie.Query(QuerySpec{
		Terms:  []string{string(RelTags)},
		Filter: string(RelAll),
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
		tags = append(tags, RowValues(row, RelAll)...)
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
		Filter:  string(RelTag),
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
	if _, err := tie.Add(string(RelTags), string(RelAll), tag); err != nil {
		return err
	}
	return nil
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
			b.Delete(subject, string(RelTag), tag)
		}
		if _, err := tie.Batch(b); err != nil {
			return 0, err
		}
	}
	reg := tie.NewBatch()
	reg.Delete(string(RelTags), string(RelAll), tag)
	if _, err := tie.Batch(reg); err != nil {
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
			b.Delete(subject, string(RelTag), oldTag)
			b.Add(subject, string(RelTag), newTag)
		}
		if _, err := tie.Batch(b); err != nil {
			return 0, err
		}
	}
	reg := tie.NewBatch()
	reg.Delete(string(RelTags), string(RelAll), oldTag)
	reg.Add(string(RelTags), string(RelAll), newTag)
	if _, err := tie.Batch(reg); err != nil {
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
		Filter:  string(RelTag),
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
		Filter:  string(RelType),
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
// server returns only rows lacking a tag. The server paginates via offset/limit.
func (tie *TieClient) untaggedOfType(scope string, offset, limit int) ([]TaggedFile, int, error) {
	rows, total, err := tie.Query(QuerySpec{
		Terms:           []string{scope},
		Filter:          string(RelType),
		MissingRelation: string(RelTag),
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
// For directories, tries RelFilename first, then RelPath (for DirUIDs), extracting
// the basename from either.
func taggedFileFrom(row Row) TaggedFile {
	isDir := RowHas(row, RelType, str(TieDirectory))
	types := row.Attributes[string(RelType)]
	isArchive := !isDir && containsArchiveType(types)
	filename := RowFirst(row, RelFilename)

	// If filename contains a path separator, extract just the basename.
	// This handles both old imports (full paths) and ensures consistency.
	if filename != "" && strings.ContainsAny(filename, "/\\") {
		filename = filepath.Base(filename)
	}

	if filename == "" && isDir {
		// Directories (especially DirUIDs) might use RelPath; extract the basename.
		if paths := RowValues(row, RelPath); len(paths) > 0 {
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

	size, _ := strconv.Atoi(RowFirst(row, RelFilesize))
	tf := TaggedFile{Hash: row.Key, Filename: filename, Size: size, IsDir: isDir, IsArchive: isArchive}
	if isArchive {
		tf.TieType = archiveTypeOf(types)
	}
	return tf
}
