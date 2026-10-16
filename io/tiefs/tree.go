// Package tiesfs is the transport-agnostic virtual filesystem that tie
// exposes: the live, tag-derived tree served by `tie mount --db` and by
// tie-smb. It carries the semantics of the three top-level views - /query (tag
// queries), /files (the path-based import tree), and /tags (writable tag/type
// text files) - plus the content-addressed blob cache and the staged write
// path, without depending on any protocol layer (go-fuse, SMB, ...).
//
// A Store owns the blob cache, stable inode numbering, and write-staging
// directory. A Tree binds a Store to a triple-store client and provides the
// operations both transports need: query execution, path-tree navigation,
// mkdir/rename, tag and dir-type editing, and staged file writes.
//
// Errors follow io/fs conventions: a missing path reports an error wrapping
// fs.ErrNotExist, a collision wraps fs.ErrExist. Transports map these onto
// their own codes (ENOENT/EEXIST for FUSE, SMB status codes for tie-smb).
package tiesfs

import (
	"hash/fnv"
	"os"
	"path"
	"strings"

	"github.com/uidbz/tie/client"
)

// Store owns the content-addressed read path (blob cache), stable inode
// numbering, and the temp dir staging in-flight writes. It needs no triple
// store, so the read-only content-addressed mount can use it alone.
type Store struct {
	cache    *Cache
	host     client.FileHost // filehost that receives uploads from the write path
	writeDir string          // temp dir staging in-flight writes (no hash until commit)
	uid      uint32          // owner uid for all files
	gid      uint32          // owner gid for all files

}

// NewStore builds a store reading blobs from host. cacheSizeGB is the on-disk
// LRU budget; verify controls hash-checking of downloaded bytes.
func NewStore(host client.FileHost, cacheSizeGB int, verify bool) *Store {
	writeDir, _ := os.MkdirTemp("", "tie-write-*")
	return &Store{
		cache:    NewCache(host.URL, client.HTTPClientFor(host), int64(cacheSizeGB)*1024*1024*1024, verify),
		host:     host,
		writeDir: writeDir,
		uid:      uint32(os.Getuid()),
		gid:      uint32(os.Getgid()),
	}
}

// Close releases the on-disk blob cache and the write-staging dir. Call once
// at shutdown.
func (s *Store) Close() error {
	if s.writeDir != "" {
		os.RemoveAll(s.writeDir)
	}
	return s.cache.Close()
}

// Cache returns the shared blob cache.
func (s *Store) Cache() *Cache { return s.cache }

// Host returns the filehost uploads go to.
func (s *Store) Host() client.FileHost { return s.host }

// UID/GID report the owner identity the transports attribute to files.
func (s *Store) UID() uint32 { return s.uid }
func (s *Store) GID() uint32 { return s.gid }

// InodeID returns the stable inode number for a key (a content hash, a DirUID,
// or a derived control-file key). It is derived from the key itself, so it is
// the same number in every mount and across restarts, and nothing is
// remembered: the map this used to be grew by one entry per distinct key ever
// listed and was never pruned, which leaked for months in a long-lived
// tie-smb mount — and because the numbers came from a per-process counter, the
// same file changed inode when the mount restarted (§4.17).
//
// Collisions are possible (64-bit FNV-1a) and tolerable: the inode is an
// identity used to deduplicate handles, and a collision makes two files look
// like hard links to one object. At library scale (10M keys) the chance of any
// collision is around one in a million. 0 is reserved ("no inode" to FUSE and
// to the SMB file ids), so it maps to 1.
func (s *Store) InodeID(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	if i := h.Sum64(); i != 0 {
		return i
	}
	return 1
}

// Tree binds a Store to a triple-store client: the live tag-derived
// filesystem.
type Tree struct {
	*Store
	Tie *client.TieClient
}

// NewTree builds the live virtual filesystem over tie, uploading writes to
// host.
func NewTree(tie *client.TieClient, host client.FileHost, cacheSizeGB int, verify bool) *Tree {
	return &Tree{Store: NewStore(host, cacheSizeGB, verify), Tie: tie}
}

// NewTreeWithStore builds a Tree over an existing Store, so a transport can
// share one cache/inode-numbering instance between its content-addressed and
// live-DB views.
func NewTreeWithStore(store *Store, tie *client.TieClient) *Tree {
	return &Tree{Store: store, Tie: tie}
}

// mediaTypeToken maps the friendly "type:" token values to their full tie-type
// value strings. Any other token value is used verbatim as the scope, so full
// stringer names ("audio-file") and custom dir-type labels ("live-album") both
// pass through unchanged.
var mediaTypeToken = map[string]string{
	"image":    client.TieImageFile.String(),
	"audio":    client.TieAudioFile.String(),
	"video":    client.TieVideoFile.String(),
	"document": client.TieDocumentFile.String(),
	"archive":  client.TieArchiveFile.String(),
}

// ParseTagQuery splits a query directory name into a tie-type scope and the
// include/exclude tag sets. Space separates terms; a leading "-" excludes; a
// "type:<name>" token sets the scope (default: "", meaning all types). <name>
// accepts a friendly form ("audio", resolved to "audio-file"), a full type
// name ("audio-file"), or a custom dir-type label ("live-album"), each of
// which is used as a raw tie-type value - unlike a media-type enum, an unknown
// label is kept literally rather than collapsed to "all", so custom labels are
// queryable.
func ParseTagQuery(query string) (scope string, include, exclude []string) {
	for _, term := range strings.Fields(query) {
		switch {
		case strings.HasPrefix(term, "type:"):
			name := strings.TrimPrefix(term, "type:")
			if resolved, ok := mediaTypeToken[name]; ok {
				scope = resolved
			} else {
				scope = name
			}
		case strings.HasPrefix(term, "-"):
			if tag := term[1:]; tag != "" {
				exclude = append(exclude, tag)
			}
		default:
			include = append(include, term)
		}
	}
	return scope, include, exclude
}

// SavedQuery resolves a saved query name from the client config to its stored
// expression.
func (t *Tree) SavedQuery(name string) (string, bool) {
	q, ok := t.Tie.Config.Queries[name]
	return q, ok
}

// QueryFiles runs a tag query and disambiguates the result so every match is
// reachable by name.
func (t *Tree) QueryFiles(scope string, include, exclude []string) ([]client.TaggedFile, error) {
	files, _, err := t.Tie.FilesWithTags(scope, include, exclude, 0, 0)
	if err != nil {
		return nil, err
	}
	return Disambiguate(files), nil
}

// TagList returns every known tag, live.
func (t *Tree) TagList() ([]string, error) {
	tags, _, err := t.Tie.ListTags(0, 0)
	if err != nil {
		return nil, err
	}
	return tags, nil
}

// TypeList returns every known dir-type classification label, live.
func (t *Tree) TypeList() ([]string, error) {
	return t.Tie.ListDirTypes()
}

// Disambiguate makes the display names within one flat query result unique so
// every match is reachable by name, not just the first. Two directories or
// files can legitimately share a name (e.g. rootA/SomeDir and rootB/SomeDir,
// or a track1.jpg in two albums); without this, a name lookup would resolve to
// the first match and shadow the rest. The first occurrence of a name keeps
// it; each later collision gets a "~<short subject>" suffix derived from the
// entry's content hash - stable across calls (the query order is
// server-sorted) and inserted before the file extension so "track1.jpg"
// becomes "track1~0f4098fb.jpg". Listing and lookup both run the query, so
// they always agree on the final names. The input slice is mutated and
// returned.
func Disambiguate(files []client.TaggedFile) []client.TaggedFile {
	seen := make(map[string]int, len(files))
	for i := range files {
		name := files[i].Filename
		if seen[name] == 0 {
			seen[name]++
			continue
		}
		seen[name]++
		files[i].Filename = SuffixName(name, files[i].Hash)
	}
	return files
}

// DisambiguateFiles uniquifies colliding filenames in a ReadTieDir file
// listing the same way Disambiguate does for TaggedFiles: the first occurrence
// keeps the bare name, later collisions get a "~<hash8>" suffix. This matters
// for a "_prev" history directory, which can hold several versions of one file
// all recorded under the same filename. ReadTieDir returns dir.Files in a
// stable order, so the suffixing is deterministic across repeated listings.
// The input slice is mutated and returned.
func DisambiguateFiles(files []client.File) []client.File {
	seen := make(map[string]int, len(files))
	for i := range files {
		name := files[i].Filename
		if seen[name] == 0 {
			seen[name]++
			continue
		}
		seen[name]++
		files[i].Filename = SuffixName(name, files[i].Uid)
	}
	return files
}

// SuffixName inserts a "~<short>" disambiguator into name, before the final
// extension if there is one, using the first 8 characters of subject (a DirUID
// or content hash). "SomeDir" -> "SomeDir~42e55dcf"; "a.jpg" ->
// "a~0f4098fb.jpg".
func SuffixName(name, subject string) string {
	short := subject
	if len(short) > 8 {
		short = short[:8]
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return base + "~" + short + ext
}

// BaseName returns the last segment of a tie:/-prefixed virtual path.
func BaseName(p string) string {
	p = strings.TrimPrefix(p, client.FileURIScheme)
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ChildPath joins a parent slash path and a child segment, avoiding a double
// slash when the parent is the root "/".
func ChildPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}
