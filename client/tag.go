package client

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~uid/tie/io/putlib"
	"git.sr.ht/~uid/tie/metadata"
	"git.sr.ht/~uid/tie/tiedb"
	"github.com/google/uuid"
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
)

const (
	FileURIScheme string = "file:"
)

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

func (tie *TieClient) ImportFile(file string, host FileHost, collection string, tags []string, directory DirUID) error {
	fmt.Println("Importing:", file)
	fileType, err := GetTieTypeFromPath(file)
	if err != nil {
		return err
	}
	stat, err := os.Stat(file)
	if err != nil {
		return err
	}
	status := putlib.Upload(host.URL, file, putlib.PutConfig{Client: httpClientFor(host)})

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
func (tie *TieClient) ImportDir(dir string, host FileHost, collection string, dirType string, tags []string, dest string) error {
	status := putlib.Upload(host.URL, dir, putlib.PutConfig{Client: httpClientFor(host)})
	if status.ErrorMsg != "" {
		return fmt.Errorf("Error uploading: %v\n%v\n", status.LastItem.Filename, status.ErrorMsg)
	}

	// Extract embedded metadata from each local file once, keyed by local path,
	// so it feeds both the root-path template (aggregated) and per-file tagging.
	fileMeta := make(map[string]metadata.Media)
	allMeta := make([]metadata.Media, 0, len(status.UploadedItems))
	for _, x := range status.UploadedItems {
		if x.MediaType == "inode/directory" {
			continue
		}
		m := ExtractMediaMetadata(x.Filename)
		fileMeta[x.Filename] = m
		allMeta = append(allMeta, m)
	}

	rootPath, err := tie.importRootPath(dir, dest, dirType, allMeta)
	if err != nil {
		return err
	}
	dirCache := make(map[string]DirUID)
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

	for _, x := range status.UploadedItems {
		rel, err := filepath.Rel(dir, x.Filename)
		if err != nil {
			return err
		}
		if x.MediaType == "inode/directory" {
			// Ensure the directory (and its ancestors) exist as DirUID entities,
			// so even childless directories are browsable.
			if _, err := dirUID(rel); err != nil {
				return err
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
		if err := Tag(tie, info, collection); err != nil {
			return err
		}
	}

	rootUID, err := dirUID(".")
	if err != nil {
		return err
	}
	return tie.SetDirType(rootUID, dirType)
}

func Tag(tie *TieClient, info TagInfo, collection string) error {
	fmt.Println("tagging", info.Hash)
	batch := tie.NewBatchIn(collection)
	filename := filepath.Base(info.File)
	hash := info.Hash
	name := strings.TrimRight(filename, filepath.Ext(filename)) // Remove extension
	batch.Add(hash, str(TieFilename), filename)
	batch.Add(hash, str(TieName), name)
	batch.Add(hash, str(TieMediaType), info.MediaType)
	batch.Add(hash, str(TieTypeProperty), str(info.TieType))
	batch.Add(hash, str(TieFilesize), strconv.Itoa(info.Size))
	batch.Add(hash, str(TieTagDate), time.Now().Format(time.DateTime))
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
	if info.IsDir {
		batch.Add(hash, str(TieTypeProperty), str(TieDirectory))
	} else {
		batch.Add(hash, str(TieTypeProperty), str(TieFile))
	}
	if info.Directory != "" {
		batch.Add(hash, str(TieParent), str(info.Directory))
	}

	r, err := tie.Batch(batch)
	if err != nil {
		return err
	}
	for _, a := range r.AddReplys {
		if !a.Success {
			return errors.New("Error tagging: " + a.OrigKey + " First error message: " + a.Message)
		}
	}

	return nil
}

func (tie *TieClient) DirUIDFromPath(path string) (DirUID, error) {
	o := GetOptions{
		Reverse: true,
		Filter:  str(TiePath),
	}
	if !strings.HasPrefix(path, FileURIScheme) {
		path = FileURIScheme + path
	}
	var uid DirUID
	r, err := tie.Get(path, o)
	if errors.Is(err, ErrNotFound) {
		return "", nil // path not tied to any UID yet
	}
	if err != nil {
		return "", errors.New("error:'" + err.Error() + "'")
	}
	if len(r.Result) > 1 {
		return "", errors.New("Multiple (" + strconv.Itoa(len(r.Result)) + ") UIDs found for path. Expected 1.")
	}
	for key := range r.Result {
		uid = DirUID(key)
	}

	return uid, nil
}

func (tie *TieClient) CreateTieRootDir() error {
	rootpath := FileURIScheme + "/"
	uid, _ := tie.DirUIDFromPath(rootpath)
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
}

func ReadTieDir(tie *TieClient, uid DirUID) (Directory, error) {
	var dir Directory
	r, err := tie.SimpleGet(string(uid))
	if err != nil {
		return dir, errors.New("error:'" + err.Error() + "'")
	}
	r.Result.ForEachKey(func(key string) {
		dir.Uid = uid
		entry := r.Result[key]
		dir.Paths = entry[str(TiePath)].ToSlice()
		parents := entry[str(TieParent)].ToSlice()
		dir.ParentUIDs = make([]DirUID, 0, len(parents))
		for _, x := range parents {
			dir.ParentUIDs = append(dir.ParentUIDs, DirUID(x))
		}
	})

	o := GetOptions{
		Reverse:      true,
		GetNextLevel: true,
	}
	r, err = tie.Get(string(uid), o)
	if errors.Is(err, ErrNotFound) {
		return dir, nil // directory has no children
	}
	if err != nil {
		return dir, errors.New("error:'" + err.Error() + "'")
	}
	r.Result.ForEachKey(func(key string) {
		meta := r.NextLevelResult[key]
		types := meta[str(TieTypeProperty)]
		switch true {
		case types.Has(str(TieDirectory)):
			subDir := SubDirectory{
				Uid:      DirUID(key),
				Paths:    meta[str(TiePath)].ToSlice(),
				DirTypes: SliceToTieType(types.ToSlice()),
			}
			dir.SubDirs = append(dir.SubDirs, subDir)
		case types.Has(str(TieImageFile)):
			fallthrough
		case types.Has(str(TieVideoFile)):
			fallthrough
		case types.Has(str(TieAudioFile)):
			fallthrough
		case types.Has(str(TieDocumentFile)):
			f := File{
				Uid:       key,
				Filename:  meta[str(TieFilename)].ToString(),
				TieType:   StringToTieType(types.ToString()),
				MediaType: meta[str(TieMediaType)].ToString(),
			}
			dir.Files = append(dir.Files, f)

		}
	})

	return dir, nil
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
}

// ListTags returns tag names known to the store, read from the
// ("tags", "all", <tag>) registry that Tag writes, ordered by tag name.
// offset/limit paginate; limit <= 0 means no limit. The second return is the
// total number of tags before pagination.
func (tie *TieClient) ListTags(offset, limit int) ([]string, int, error) {
	o := GetOptions{Filter: str(TieAll)}
	o.Sort = tiedb.SortOptions{Offset: offset, Limit: limit}
	r, err := tie.Get(str(TieTags), o)
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var tags []string
	for _, t := range r.SortedResult {
		tags = append(tags, t.Value2)
	}
	return tags, r.TotalCount, nil
}

// FilesWithTag returns the files tagged with tag, ordered by content hash.
// Files are stored as forward triples (hash, "tag", tag), so a reverse lookup on
// the tag name yields the hashes; GetNextLevel pulls each hash's filename and
// size in the same call. offset/limit paginate; limit <= 0 means no limit. The
// second return is the total number of files before pagination.
func (tie *TieClient) FilesWithTag(tag string, offset, limit int) ([]TaggedFile, int, error) {
	o := GetOptions{
		Reverse:      true,
		Filter:       str(TieTag),
		GetNextLevel: true,
	}
	o.Sort = tiedb.SortOptions{Offset: offset, Limit: limit}
	r, err := tie.Get(tag, o)
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var files []TaggedFile
	for _, t := range r.SortedResult {
		files = append(files, taggedFileFrom(t.Key, r.NextLevelResult[t.Key]))
	}
	return files, r.TotalCount, nil
}

// FilesWithTags returns the files that carry ALL of include and NONE of exclude,
// scoped to a single media type (e.g. TieAudioFile for "find music with tag1,
// tag2 but not tag4"). Tags share the "tag" relation so they AND/NOT together
// inside one QueryTags call; the media-type scoping keys on a different relation
// (tie-type), so it rides along as the query's Scope, which the server intersects
// by hash identity — no client-side filtering. When include is empty the whole
// media type is browsed directly. The server paginates via offset/limit; limit
// <= 0 means no limit. The second return is the total number of matching files
// before pagination.
func (tie *TieClient) FilesWithTags(mediaType TieType, include, exclude []string, offset, limit int) ([]TaggedFile, int, error) {
	// With no tags, browse the whole media type directly.
	if len(include) == 0 {
		return tie.filesOfType(mediaType, offset, limit)
	}

	o := GetOptions{
		Reverse:      true,
		Filter:       str(TieTag),
		Include:      include[1:],
		Exclude:      exclude,
		Scope:        str(mediaType),
		GetNextLevel: true,
	}
	o.Sort = tiedb.SortOptions{Offset: offset, Limit: limit}
	r, err := tie.Get(include[0], o)
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}

	var files []TaggedFile
	for _, t := range r.SortedResult {
		files = append(files, taggedFileFrom(t.Key, r.NextLevelResult[t.Key]))
	}
	return files, r.TotalCount, nil
}

// filesOfType builds TaggedFiles for every hash of a media type, pulling per-hash
// metadata via a reverse GetNextLevel lookup on the tie-type value. The server
// paginates via offset/limit.
func (tie *TieClient) filesOfType(mediaType TieType, offset, limit int) ([]TaggedFile, int, error) {
	o := GetOptions{Reverse: true, Filter: str(TieTypeProperty), GetNextLevel: true}
	o.Sort = tiedb.SortOptions{Offset: offset, Limit: limit}
	r, err := tie.Get(str(mediaType), o)
	if errors.Is(err, ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var files []TaggedFile
	for _, t := range r.SortedResult {
		files = append(files, taggedFileFrom(t.Key, r.NextLevelResult[t.Key]))
	}
	return files, r.TotalCount, nil
}

// taggedFileFrom builds a TaggedFile from a hash and its next-level metadata,
// falling back to the hash as the display name when no filename is recorded.
func taggedFileFrom(hash string, meta tiedb.Value1) TaggedFile {
	filename := meta[str(TieFilename)].ToString()
	if filename == "" {
		filename = hash
	}
	size, _ := strconv.Atoi(meta[str(TieFilesize)].ToString())
	isDir := meta[str(TieTypeProperty)].Has(str(TieDirectory))
	return TaggedFile{Hash: hash, Filename: filename, Size: size, IsDir: isDir}
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
	r, err := tie.Batch(b)
	if err != nil {
		return err
	}
	for _, a := range r.AddReplys {
		if !a.Success {
			return errors.New("RelateFiles: " + a.Message)
		}
	}
	return nil
}

// RelationsFrom returns every media-to-media relation recorded on hash. Because
// RelateFiles writes both directions as forward edges, this one forward lookup
// surfaces relations pointing both from and into hash. Only triples whose object
// is itself a content hash are returned, so file metadata (filename, tag, ...) is
// excluded.
func (tie *TieClient) RelationsFrom(hash string) ([]MediaRelation, error) {
	r, err := tie.SimpleGet(hash)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rels []MediaRelation
	r.Result.ForEachValue2(func(key, relation, value2 string) {
		if metadata.IsHexHash(value2) {
			rels = append(rels, MediaRelation{FromHash: key, Relation: relation, ToHash: value2})
		}
	})
	return rels, nil
}

// tieDirAncestors returns the virtual directory paths that make up p, from the
// top-level directory down to p itself, each prefixed with FileURIScheme. The
// root "file:/" is not included (it is created separately). Virtual paths are
// always absolute, so a leading slash is assumed and the input is cleaned to
// collapse "." / ".." and redundant separators.
//
//	"file:/a/b/c" -> ["file:/a", "file:/a/b", "file:/a/b/c"]
//	"/a/b"        -> ["file:/a", "file:/a/b"]
//	"file:/"      -> [] (root only)
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
	uid, _ := tie.DirUIDFromPath(path)
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

func (tie *TieClient) SetDirType(uid DirUID, dirType string) error {
	_, err := tie.Add(str(uid), str(TieTypeProperty), dirType)
	return err
}

func (tie *TieClient) newDirUID() DirUID {
	return DirUID(uuid.Must(uuid.NewV7()).String())
}
