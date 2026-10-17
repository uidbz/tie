package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/tiedb"
)

// The virtual directory tree: creating, reading, renaming and deleting tie
// directories, minting DirUIDs, and mapping a virtual path to one. Split out of
// tag.go (§7.11).

func (tie *TieClient) DirUIDFromPath(path string) (DirUID, error) {
	if !strings.HasPrefix(path, FileURIScheme) {
		path = FileURIScheme + path
	}
	rows, _, err := tie.Query(QuerySpec{Terms: []string{path}, Reverse: true, Filter: string(RelPath)})
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
		tie.log().Warn("multiple UIDs found for path, expected 1 — run the dedup script to fix",
			"count", len(rows), "path", path, "using", rows[0].Key)
	}
	var uid DirUID
	for _, row := range rows {
		uid = DirUID(row.Key)
		break
	}

	return uid, nil
}

// tieRootDir returns the UID of the collection's root directory, creating it
// when absent. The root is its own parent, so one MkDir covers it; the reply
// says whether it was already there.
func (tie *TieClient) tieRootDir() (DirUID, bool, error) {
	newUID := tie.newDirUID()
	uid, existed, err := tie.mkdir(newUID, FileURIScheme+"/", newUID)
	return uid, existed, err
}

// CreateTieRootDir creates the collection's root directory. It returns an
// error wrapping ErrDirExists when the root is already there, naming its UID.
func (tie *TieClient) CreateTieRootDir() error {
	uid, existed, err := tie.tieRootDir()
	if err != nil {
		return err
	}
	if existed {
		return fmt.Errorf("root dir already exists, with UID: %s: %w", uid, ErrDirExists)
	}
	return nil
}

func ReadTieDir(tie *TieClient, uid DirUID) (Directory, error) {
	var dir Directory
	self, err := tie.Get(string(uid))
	if err != nil {
		return dir, errors.New("error:'" + err.Error() + "'")
	}
	dir.Uid = uid
	dir.Paths = RowValues(self, RelPath)
	parents := RowValues(self, RelParent)
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
		types := row.Attributes[string(RelType)]
		switch true {
		case slices.Contains(types, str(TieDirectory)):
			subDir := SubDirectory{
				Uid:      DirUID(row.Key),
				Paths:    RowValues(row, RelPath),
				DirTypes: SliceToTieType(types),
			}
			dir.SubDirs = append(dir.SubDirs, subDir)
		case containsArchiveType(types):
			size, _ := strconv.Atoi(RowFirst(row, RelFilesize))
			filename := RowFirst(row, RelFilename)
			if filename == "" {
				filename = row.Key
			}
			dir.Archives = append(dir.Archives, ArchiveEntry{
				Filename: filename,
				Hash:     row.Key,
				TieType:  archiveTypeOf(types),
				Size:     size,
				TagDate:  parseTagDate(RowFirst(row, RelTagDate)),
			})
		default:
			// Any child that is neither a subdirectory nor a browsable archive is
			// a plain file. This covers every media type (image/video/audio/
			// document) and, crucially, structural "file"/"unknown-file" children
			// (e.g. a copied-in .txt): without this case they were stored but
			// invisible in every listing. Since children reach a DirUID only via a
			// parent edge (dir/archive/file), a default match cannot pick up a
			// non-child row.
			size, _ := strconv.Atoi(RowFirst(row, RelFilesize))
			filename := RowFirst(row, RelFilename)
			if filename == "" {
				filename = row.Key // fall back to the hash when no filename is recorded
			}
			f := File{
				Uid:       row.Key,
				Filename:  filename,
				TieType:   StringToTieType(strings.Join(types, ", ")),
				MediaType: RowFirst(row, RelMediaType),
				Size:      size,
				TagDate:   parseTagDate(RowFirst(row, RelTagDate)),
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
	oldFilename := RowFirst(row, RelFilename)
	oldName := RowFirst(row, RelName)

	batch := tie.NewBatch()
	if newName != oldFilename {
		newBase := nameFromFilename(newName)
		// AddOnFailure so a file that never recorded a filename (the
		// hash-fallback case in ReadTieDir) still gets one.
		fn := tie.NewUpdate(hash, string(RelFilename), oldFilename, newName)
		fn.AddOnFailure = true
		batch.Update(fn)
		nm := tie.NewUpdate(hash, string(RelName), oldName, newBase)
		nm.AddOnFailure = true
		batch.Update(nm)
	}
	if newParent != oldParent {
		// parent is potentially multi-valued (same content in several dirs),
		// so delete the specific old edge rather than blanket-updating.
		batch.Delete(hash, string(RelParent), str(oldParent))
		batch.Add(hash, string(RelParent), str(newParent))
	}
	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return nil
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
	own := tie.NewUpdate(str(uid), string(RelPath), oldPath, newPath)
	own.AddOnFailure = true
	batch.Update(own)

	for _, d := range descendants {
		for _, p := range d.Paths {
			if !strings.HasPrefix(p, oldPath) {
				continue
			}
			np := newPath + strings.TrimPrefix(p, oldPath)
			du := tie.NewUpdate(str(d.Uid), string(RelPath), p, np)
			du.AddOnFailure = true
			batch.Update(du)
		}
	}

	if newParent != oldParent {
		batch.Delete(str(uid), string(RelParent), str(oldParent))
		batch.Add(str(uid), string(RelParent), str(newParent))
	}

	if _, err := tie.Batch(batch); err != nil {
		return err
	}
	return nil
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

// DeleteFile unlinks a file from one directory: it removes the
// (hash,"parent",parent) edge. If that was the hash's last parent, its
// per-hash metadata (filename/name/tie-type/tags/...) is dropped too, since the
// content is no longer reachable from any directory — the same shared-content
// guard the import reconciliation uses (detachChild). The blob itself stays in
// the filehost; its retention policy governs when the bytes go.
func DeleteFile(tie *TieClient, hash string, parent DirUID) error {
	return detachChild(tie, hash, parent)
}

// DeleteDir removes a directory from the path tree: it deletes the
// (uid,"parent",parent) edge and the directory's (uid,"path",*) triples. The
// directory must be empty (no subdirs, files, or archives); a non-empty
// directory returns an error so callers surface "directory not empty" rather
// than silently orphaning children. When the last parent edge is gone the
// directory's own metadata triples are dropped as well, so the consistency
// checker sees no orphaned directory.
func DeleteDir(tie *TieClient, uid DirUID, parent DirUID) error {
	dir, err := ReadTieDir(tie, uid)
	if err != nil {
		return err
	}
	if len(dir.SubDirs) > 0 || len(dir.Files) > 0 || len(dir.Archives) > 0 {
		return fmt.Errorf("directory %s is not empty", uid)
	}

	batch := tie.NewBatch()
	batch.Delete(str(uid), string(RelParent), str(parent))
	for _, p := range dir.Paths {
		batch.Delete(str(uid), string(RelPath), p)
	}
	if _, err := tie.Batch(batch); err != nil {
		return err
	}

	n, err := parentCount(tie, string(uid))
	if err != nil {
		return err
	}
	if n > 0 {
		return nil // still reachable through another parent; keep its metadata
	}
	row, err := tie.Get(string(uid))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	meta := tie.NewBatch()
	for relation, values := range row.Attributes {
		for _, value := range values {
			meta.Delete(string(uid), relation, value)
		}
	}
	if _, err := tie.Batch(meta); err != nil {
		return err
	}
	return nil
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

// MkTieDirAll creates every missing directory of dirPath, top-down, and returns
// the UID of the deepest one.
//
// Each level is one atomic MkDir whose parent is the level above, so the walk
// costs one request per level: it no longer asks for the UID of every ancestor
// twice per call (the old version cost up to three round trips per level even
// when the whole chain already existed, §4.10).
func (tie *TieClient) MkTieDirAll(dirPath string) (DirUID, error) {
	parentUID, _, err := tie.tieRootDir()
	if err != nil {
		return "", err
	}
	uid := parentUID
	for _, ancestor := range tieDirAncestors(dirPath) {
		next, _, err := tie.mkdir(parentUID, ancestor, tie.newDirUID())
		if err != nil {
			return "", err
		}
		parentUID = next
		uid = next
	}
	return uid, nil
}

// MkTieDir creates one directory; its parent must already exist (use
// MkTieDirAll to create a chain). The check for an existing node and the
// creation are one server-side step, so two concurrent creators end up sharing
// one node rather than minting two for the same path.
//
// When the path is already owned, the existing UID is returned along with an
// error wrapping ErrDirExists.
func (tie *TieClient) MkTieDir(dirPath string) (DirUID, error) {
	if !strings.HasPrefix(dirPath, FileURIScheme) {
		dirPath = FileURIScheme + dirPath
	}
	parentPath := FileURIScheme + virtualDir(dirPath)
	parentUID, err := tie.DirUIDFromPath(parentPath)
	if err != nil {
		return "", err
	}
	if parentUID == "" {
		return "", fmt.Errorf("cannot create directory '%s': no such parent '%s'", dirPath, parentPath)
	}

	uid, existed, err := tie.mkdir(parentUID, dirPath, tie.newDirUID())
	if err != nil {
		return uid, err
	}
	if existed {
		return uid, fmt.Errorf("cannot create directory '%s': %w", dirPath, ErrDirExists)
	}
	return uid, nil
}

// mkdir is the atomic create: the server looks the path up and writes the three
// triples under newUID only if no subject owns the path, all under one
// changeMutex hold. It returns the UID that owns the path and whether it was
// already there — which is the other creator's UID when a concurrent request
// won, so the caller never has to re-query or guess.
func (tie *TieClient) mkdir(parentUID DirUID, dirPath string, newUID DirUID) (DirUID, bool, error) {
	request := tie.CollectionInfo().NewMkDirRequest(tiedb.DirSpec{
		Path:           dirPath,
		NewUID:         string(newUID),
		ParentUID:      string(parentUID),
		PathRelation:   string(RelPath),
		ParentRelation: string(RelParent),
		TypeRelation:   string(RelType),
		TypeValue:      str(TieDirectory),
	})
	reply, err := run[api.MkDirReply](tie, request)
	if err != nil {
		return "", false, err
	}
	if err := replyError(reply.ReplyStatus); err != nil {
		return "", false, err
	}
	return DirUID(reply.UID), reply.Existed, nil
}

// virtualDir is the directory above a virtual tie path, without the scheme:
// "/a/b" -> "/a", "/a" -> "/". Virtual paths are always slash-separated, so
// this must not use filepath: on Windows filepath.Dir would split on '\' and
// hand the triplestore a parent path it has never seen (§4.10).
func virtualDir(virtualPath string) string {
	p := strings.TrimPrefix(virtualPath, FileURIScheme)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	dir := path.Dir(p)
	if dir == "." || dir == "/" {
		return "/"
	}
	return dir
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
