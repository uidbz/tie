package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~uid/tie/io/putlib"
	"git.sr.ht/~uid/tie/metadata"
	"git.sr.ht/~uid/tie/tiedb"
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
	TieTiedirHash                      // tiedir-hash
	TieDirUID                          // dir-uid
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
			uid, err := dirUID(rel)
			if err != nil {
				return err
			}
			// Tag the directory's tiedir content hash so it appears in type/tag
			// queries. The directory's TieType is already set via MkTieDir and
			// SetDirType, but the tiedir blob itself needs to be tagged with the
			// directory's tags so it's discoverable and expandable.
			// Use filepath.Base to get just the directory name, not the full path.
			dirname := filepath.Base(rel)
			if dirname == "." {
				// Root directory of the import; use the last segment of the virtual path.
				dirname = filepath.Base(strings.TrimPrefix(rootPath, FileURIScheme))
			}
			info := TagInfo{
				Hash:      x.Hash,
				File:      dirname, // Just the directory name for display
				Size:      x.Size,
				MediaType: x.MediaType,
				Directory: uid, // The tiedir blob's parent is itself (self-reference).
				TieType:   TieDirectory,
				Tags:      tags,
				Metadata:  metadata.Media{}, // Directories don't have embedded metadata.
			}
			if err := Tag(tie, info, collection); err != nil {
				return err
			}
			// Link the tiedir content hash to the DirUID so the directory is
			// expandable in content-addressed contexts (e.g. tag query results).
			// This bidirectional association lets the FUSE layer resolve a DirUID
			// to its tiedir blob when entering a directory from a type query.
			batch := tie.NewBatchIn(collection)
			batch.Add(string(uid), str(TieTiedirHash), x.Hash)
			batch.Add(x.Hash, str(TieDirUID), string(uid))
			if _, err := tie.Batch(batch); err != nil {
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
	Size      int
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
			size, _ := strconv.Atoi(meta[str(TieFilesize)].ToString())
			filename := meta[str(TieFilename)].ToString()
			if filename == "" {
				filename = key // fall back to the hash when no filename is recorded
			}
			f := File{
				Uid:       key,
				Filename:  filename,
				TieType:   StringToTieType(types.ToString()),
				MediaType: meta[str(TieMediaType)].ToString(),
				Size:      size,
			}
			dir.Files = append(dir.Files, f)

		}
	})

	return dir, nil
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
	r, err := tie.SimpleGet(hash)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	var oldFilename, oldName string
	if r.Result != nil {
		entry := r.Result[hash]
		oldFilename = entry[str(TieFilename)].ToString()
		oldName = entry[str(TieName)].ToString()
	}

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
	r, err := tie.SimpleGet(hash)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tags := r.Result[hash][str(TieTag)].ToSlice()
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

	o := GetOptions{
		Reverse:      true,
		Filter:       str(TieTag),
		Include:      include[1:],
		Exclude:      exclude,
		Scope:        scope,
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

// filesOfType builds TaggedFiles for every hash carrying the tie-type value
// scope, pulling per-hash metadata via a reverse GetNextLevel lookup. The server
// paginates via offset/limit.
func (tie *TieClient) filesOfType(scope string, offset, limit int) ([]TaggedFile, int, error) {
	o := GetOptions{Reverse: true, Filter: str(TieTypeProperty), GetNextLevel: true}
	o.Sort = tiedb.SortOptions{Offset: offset, Limit: limit}
	r, err := tie.Get(scope, o)
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
// For directories, tries TieFilename first, then TiePath (for DirUIDs), extracting
// the basename from either.
func taggedFileFrom(hash string, meta tiedb.Value1) TaggedFile {
	isDir := meta[str(TieTypeProperty)].Has(str(TieDirectory))
	filename := meta[str(TieFilename)].ToString()
	
	// If filename contains a path separator, extract just the basename.
	// This handles both old imports (full paths) and ensures consistency.
	if filename != "" && strings.ContainsAny(filename, "/\\") {
		filename = filepath.Base(filename)
	}
	
	if filename == "" && isDir {
		// Directories (especially DirUIDs) might use TiePath; extract the basename.
		if paths := meta[str(TiePath)].ToSlice(); len(paths) > 0 {
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
		filename = hash
	}
	
	size, _ := strconv.Atoi(meta[str(TieFilesize)].ToString())
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
	r, err := tie.SimpleGet(string(uid))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var labels []string
	for _, t := range r.Result[string(uid)][str(TieTypeProperty)].ToSlice() {
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
	r, err := tie.Get(typeRegistrySubject, GetOptions{Filter: str(TieAll)})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err == nil {
		r.Result.ForEachValue2(func(_, _, value2 string) {
			set[value2] = true
		})
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
