package fuselib

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"

	"github.com/uidbz/tie/client"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// TieDBFuse serves a live, mutable virtual filesystem derived from the triple
// store. The layout is:
//
//	/query                      README + "tags" and "types" listings
//	/query/tags                 newline-separated list of every known tag
//	/query/types                newline-separated list of every known dir-type label
//	/query/<query>/<filename>   files matching a tag query; the directory name is
//	                            the query itself, e.g. "jazz mellow -live" ANDs
//	                            jazz and mellow, excludes live. A "type:audio"
//	                            token scopes to a tie-type (default: all types);
//	                            a custom dir-type label (e.g. "type:live-album")
//	                            works too.
//	/files/<path>/...           the path-based virtual directory tree (tie:/...)
//	/tags/<path>/...            mirrors /files, but each leaf is a small writable
//	                            text file whose contents are the file's tags (one
//	                            per line). Editing the file re-tags the content.
//
// Files serve their bytes from the filehost by hash; tagged directories expand
// as immutable content-addressed trees (tiedir blobs). Unlike the
// content-addressed Mount, this reflects the store as-is on every readdir:
// re-tagging is visible without remounting.
type TieDBFuse struct {
	tie      *client.TieClient
	fuseTree *TieFuse        // reused for content-addressed file/dir bytes + cache
	host     client.FileHost // filehost that receives uploads from the write path
	writeDir string          // temp dir staging in-flight writes (no hash until commit)
	uid      uint32          // owner uid for all files
	gid      uint32          // owner gid for all files
}

func NewTieDBFuse(tie *client.TieClient, host client.FileHost, cacheSizeGB int, verify bool) *TieDBFuse {
	writeDir, _ := os.MkdirTemp("", "tie-fuse-write-*")
	return &TieDBFuse{
		tie:      tie,
		fuseTree: NewTieFuse(host, cacheSizeGB, verify),
		host:     host,
		writeDir: writeDir,
		uid:      uint32(os.Getuid()),
		gid:      uint32(os.Getgid()),
	}
}

// Close releases the on-disk blob cache and the write-staging dir. Call once
// after the mount is torn down.
func (state *TieDBFuse) Close() error {
	if state.writeDir != "" {
		os.RemoveAll(state.writeDir)
	}
	return state.fuseTree.Close()
}

const tagHelpText = `tie tag-query filesystem
=========================

Navigate to a directory whose name is a tag query to list the matching files:

    ls "query/jazz"                 files tagged jazz
    ls "query/jazz mellow"          tagged jazz AND mellow
    ls "query/jazz mellow -live"    tagged jazz AND mellow, but NOT live

Space separates terms (all ANDed). A leading "-" excludes a tag.

Scope to a tie-type with a "type:" token (default: all types). This accepts a
media type, a full type name, or a custom directory label:

    ls "query/mellow type:audio"    audio files tagged mellow
    ls "query/type:image"           all image files
    ls "query/type:live-album"      dirs carrying the custom "live-album" label

List every known tag, or every known dir-type label:

    cat query/tags
    cat query/types

Saved queries from your config's [Queries] table appear here as ready-made
directories, e.g. a "chill-jazz = jazz mellow -live" entry gives:

    ls query/chill-jazz

The other top-level directory, files/<path>/, is the path-based virtual
directory tree.
`

// dbRoot lists the top-level virtual directories.
type dbRoot struct {
	fs.Inode
	state *TieDBFuse
}

var (
	_ = (fs.NodeReaddirer)((*dbRoot)(nil))
	_ = (fs.NodeLookuper)((*dbRoot)(nil))
)

func (r *dbRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: "query", Mode: fuse.S_IFDIR},
		{Name: "files", Mode: fuse.S_IFDIR},
		{Name: "tags", Mode: fuse.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (r *dbRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "query":
		child := &tagQueryRoot{state: r.state}
		return r.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
	case "files":
		child := &pathDir{state: r.state, path: "/"}
		return r.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
	case "tags":
		child := &tagsDir{state: r.state, path: "/"}
		return r.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
	}
	return nil, syscall.ENOENT
}

// tagQueryRoot is the /query directory. It cannot enumerate every possible
// query, so a readdir shows only three helper files: a README explaining the
// syntax, a "tags" listing of every known tag, and a "types" listing of every
// known dir-type label. Any other name is treated as a query string and resolved
// live.
type tagQueryRoot struct {
	fs.Inode
	state *TieDBFuse
}

var (
	_ = (fs.NodeReaddirer)((*tagQueryRoot)(nil))
	_ = (fs.NodeLookuper)((*tagQueryRoot)(nil))
)

const (
	tagHelpName  = "README"
	tagListName  = "tags"
	typeListName = "types"
)

func (q *tagQueryRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: tagHelpName, Mode: fuse.S_IFREG},
		{Name: tagListName, Mode: fuse.S_IFREG},
		{Name: typeListName, Mode: fuse.S_IFREG},
	}
	// Saved queries from config appear as ready-made query directories.
	for name := range q.state.tie.Config.Queries {
		entries = append(entries, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR})
	}
	return fs.NewListDirStream(entries), 0
}

func (q *tagQueryRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case tagHelpName:
		child := &staticFile{data: []byte(tagHelpText)}
		out.Size = uint64(len(child.data))
		return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	case tagListName:
		child := &tagListFile{state: q.state}
		return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	case typeListName:
		child := &typeListFile{state: q.state}
		return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	}
	// A saved query name resolves to its stored expression; otherwise the name
	// is itself treated as an ad-hoc query.
	query := name
	if saved, ok := q.state.tie.Config.Queries[name]; ok {
		query = saved
	}
	// Validate the query resolves before materializing the inode so a bogus
	// query returns ENOENT rather than an empty directory.
	scope, include, exclude := parseTagQuery(query)
	if len(include) == 0 && scope == "" {
		return nil, syscall.ENOENT
	}
	child := &tagQueryDir{state: q.state, scope: scope, include: include, exclude: exclude}
	return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
}

// tagListFile serves the newline-separated list of every known tag, fetched
// live on each open so a newly added tag shows up without remounting.
type tagListFile struct {
	fs.Inode
	state *TieDBFuse
}

var (
	_ = (fs.NodeGetattrer)((*tagListFile)(nil))
	_ = (fs.NodeOpener)((*tagListFile)(nil))
)

func (f *tagListFile) contents() ([]byte, syscall.Errno) {
	tags, _, err := f.state.tie.ListTags(0, 0)
	if err != nil {
		return nil, syscall.EIO
	}
	if len(tags) == 0 {
		return nil, 0
	}
	return []byte(strings.Join(tags, "\n") + "\n"), 0
}

func (f *tagListFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	data, errno := f.contents()
	if errno != 0 {
		return errno
	}
	out.Size = uint64(len(data))
	out.Mode = 0444 // Readonly regular file
	out.Uid = f.state.uid
	out.Gid = f.state.gid
	return 0
}

// Open snapshots the current tag list into a read-only handle, so the bytes
// stay consistent for the duration of one open.
func (f *tagListFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_RDWR|syscall.O_WRONLY) != 0 {
		return nil, 0, syscall.EROFS
	}
	data, errno := f.contents()
	if errno != 0 {
		return nil, 0, errno
	}
	return &staticFileHandle{data: data}, 0, 0
}

// typeListFile serves the newline-separated list of every known dir-type
// classification label (built-in directory types unioned with the custom labels
// registered via .type), fetched live on each open so a newly created label shows
// up without remounting.
type typeListFile struct {
	fs.Inode
	state *TieDBFuse
}

var (
	_ = (fs.NodeGetattrer)((*typeListFile)(nil))
	_ = (fs.NodeOpener)((*typeListFile)(nil))
)

func (f *typeListFile) contents() ([]byte, syscall.Errno) {
	types, err := f.state.tie.ListDirTypes()
	if err != nil {
		return nil, syscall.EIO
	}
	if len(types) == 0 {
		return nil, 0
	}
	return []byte(strings.Join(types, "\n") + "\n"), 0
}

func (f *typeListFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	data, errno := f.contents()
	if errno != 0 {
		return errno
	}
	out.Size = uint64(len(data))
	out.Mode = 0444 // Readonly regular file
	out.Uid = f.state.uid
	out.Gid = f.state.gid
	return 0
}

// Open snapshots the current type list into a read-only handle, so the bytes stay
// consistent for the duration of one open.
func (f *typeListFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_RDWR|syscall.O_WRONLY) != 0 {
		return nil, 0, syscall.EROFS
	}
	data, errno := f.contents()
	if errno != 0 {
		return nil, 0, errno
	}
	return &staticFileHandle{data: data}, 0, 0
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

// parseTagQuery splits a query directory name into a tie-type scope and the
// include/exclude tag sets. Space separates terms; a leading "-" excludes; a
// "type:<name>" token sets the scope (default: "", meaning all types). <name>
// accepts a friendly form ("audio", resolved to "audio-file"), a full type name
// ("audio-file"), or a custom dir-type label ("live-album"), each of which is
// used as a raw tie-type value — unlike a media-type enum, an unknown label is
// kept literally rather than collapsed to "all", so custom labels are queryable.
func parseTagQuery(query string) (scope string, include, exclude []string) {
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

// tagQueryDir lists the files matching one tag query.
type tagQueryDir struct {
	fs.Inode
	state   *TieDBFuse
	scope   string
	include []string
	exclude []string
}

var (
	_ = (fs.NodeReaddirer)((*tagQueryDir)(nil))
	_ = (fs.NodeLookuper)((*tagQueryDir)(nil))
)

func (d *tagQueryDir) query() ([]client.TaggedFile, error) {
	files, _, err := d.state.tie.FilesWithTags(d.scope, d.include, d.exclude, 0, 0)
	if err != nil {
		return nil, err
	}
	return disambiguate(files), nil
}

// disambiguate makes the display names within one flat query result unique so
// every match is reachable by name, not just the first. Two directories or files
// can legitimately share a name (e.g. rootA/SomeDir and rootB/SomeDir, or a
// track1.jpg in two albums); without this, Lookup would resolve the name to the
// first match and shadow the rest. The first occurrence of a name keeps it; each
// later collision gets a "~<short subject>" suffix derived from the entry's DirUID
// or content hash — stable across calls (the query order is server-sorted) and
// inserted before the file extension so "track1.jpg" becomes "track1~0f4098fb.jpg".
// Readdir and Lookup both call query(), so they always agree on the final names.
func disambiguate(files []client.TaggedFile) []client.TaggedFile {
	seen := make(map[string]int, len(files))
	for i := range files {
		name := files[i].Filename
		if seen[name] == 0 {
			seen[name]++
			continue
		}
		seen[name]++
		files[i].Filename = suffixName(name, files[i].Hash)
	}
	return files
}

// disambiguateFiles uniquifies colliding filenames in a ReadTieDir file listing
// the same way disambiguate does for TaggedFiles: the first occurrence keeps the
// bare name, later collisions get a "~<hash8>" suffix. This matters for a "_prev"
// history directory, which can hold several versions of one file all recorded
// under the same filename. ReadTieDir returns dir.Files in a stable order, so the
// suffixing is deterministic across Readdir/Lookup. The input slice is mutated and
// returned.
func disambiguateFiles(files []client.File) []client.File {
	seen := make(map[string]int, len(files))
	for i := range files {
		name := files[i].Filename
		if seen[name] == 0 {
			seen[name]++
			continue
		}
		seen[name]++
		files[i].Filename = suffixName(name, files[i].Uid)
	}
	return files
}

// fileNode builds a content-addressed file node for a directory child, carrying
// its import date so Getattr can report it as the file's mtime.
func fileNode(state *TieDBFuse, f client.File) *node {
	return &node{
		Hash:  f.Uid,
		Size:  f.Size,
		Mode:  fuse.S_IFREG,
		State: state.fuseTree,
		Mtime: f.TagDate,
	}
}

// dbFileNode is a writable file in the /files path tree. Reads serve the stored
// content by hash from the shared blob cache (identical to the read-only path);
// a writable open stages bytes to a temp file and, on close, uploads them and
// rewrites the file's triples via client.WriteFile (versioning any prior content
// into <name>_prev). file.Uid is empty for a not-yet-committed Create.
type dbFileNode struct {
	fs.Inode
	state      *TieDBFuse
	parentPath string      // slash path of the containing directory (e.g. "/music")
	name       string      // this file's name within parentPath
	file       client.File // current stored file; zero Uid before the first commit

	mu        sync.Mutex
	truncated bool // a SETATTR(size=0) arrived before the next writable Open
}

var (
	_ = (fs.NodeOpener)((*dbFileNode)(nil))
	_ = (fs.NodeGetattrer)((*dbFileNode)(nil))
	_ = (fs.NodeSetattrer)((*dbFileNode)(nil))
)

func (n *dbFileNode) newWriteHandle() (*writeHandle, error) {
	tmp, err := os.CreateTemp(n.state.writeDir, "w-*")
	if err != nil {
		return nil, err
	}
	return &writeHandle{node: n, tmp: tmp, tmpPath: tmp.Name()}, nil
}

// seed copies the current stored content into tmp so a partial in-place edit
// (write at an offset without a preceding truncate) preserves the untouched
// bytes. Bounded memory: io.Copy streams through its buffer.
func (n *dbFileNode) seed(tmp *os.File) error {
	e, err := n.state.fuseTree.cache.blob(n.file.Uid)
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, io.NewSectionReader(e.file, 0, e.size))
	return err
}

func (n *dbFileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0644 | fuse.S_IFREG
	out.Uid = n.state.uid
	out.Gid = n.state.gid
	if wh, ok := fh.(*writeHandle); ok {
		if fi, err := wh.tmp.Stat(); err == nil {
			out.Size = uint64(fi.Size())
		}
	} else {
		out.Size = uint64(n.file.Size)
	}
	if !n.file.TagDate.IsZero() {
		out.SetTimes(nil, &n.file.TagDate, &n.file.TagDate)
	}
	return 0
}

// Setattr handles the truncate the kernel issues before a rewrite open. go-fuse
// does not advertise ATOMIC_O_TRUNC, so O_TRUNC arrives as a SETATTR(size=0)
// before Open rather than as an open flag (same gotcha metaFile handles). When a
// write handle is already open the temp file is truncated directly; otherwise
// the request is remembered so the next writable Open starts empty instead of
// seeding the old content. Non-size attrs (chmod/utimes) are accepted as no-ops.
func (n *dbFileNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		if wh, ok := fh.(*writeHandle); ok {
			if err := wh.tmp.Truncate(int64(sz)); err != nil {
				return syscall.EIO
			}
			wh.dirty = true
		} else if sz == 0 {
			n.mu.Lock()
			n.truncated = true
			n.mu.Unlock()
		}
		out.Size = sz
		return 0
	}
	out.Size = uint64(n.file.Size)
	return 0
}

// Open serves reads from the shared blob cache and writes through a temp-file
// staging handle. FOPEN_DIRECT_IO on write keeps the kernel from caching a stale
// size across the truncate/rewrite (same reason as metaFile).
func (n *dbFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) == 0 {
		if n.file.Uid == "" {
			return &writeHandle{}, fuse.FOPEN_DIRECT_IO, 0 // uncommitted create; nothing to read yet
		}
		return &bytesFileHandle{hash: n.file.Uid, cache: n.state.fuseTree.cache}, fuse.FOPEN_KEEP_CACHE, 0
	}
	wh, err := n.newWriteHandle()
	if err != nil {
		return nil, 0, syscall.EIO
	}
	n.mu.Lock()
	truncated := n.truncated
	n.truncated = false
	n.mu.Unlock()
	if !truncated && n.file.Uid != "" {
		if err := n.seed(wh.tmp); err != nil {
			wh.tmp.Close()
			os.Remove(wh.tmpPath)
			return nil, 0, syscall.EIO
		}
	}
	return wh, fuse.FOPEN_DIRECT_IO, 0
}

// writeHandle stages a file's bytes to a temp file and commits them on close.
// Backing the buffer with a file (not memory) keeps large writes memory-bounded,
// like the read cache.
type writeHandle struct {
	node    *dbFileNode
	tmp     *os.File
	tmpPath string

	mu        sync.Mutex
	dirty     bool
	committed bool
}

var (
	_ = (fs.FileWriter)((*writeHandle)(nil))
	_ = (fs.FileReader)((*writeHandle)(nil))
	_ = (fs.FileFlusher)((*writeHandle)(nil))
	_ = (fs.FileReleaser)((*writeHandle)(nil))
	_ = (fs.FileFsyncer)((*writeHandle)(nil))
)

func (h *writeHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tmp == nil {
		return 0, syscall.EBADF
	}
	written, err := h.tmp.WriteAt(data, off)
	if err != nil {
		return uint32(written), syscall.EIO
	}
	h.dirty = true
	return uint32(written), 0
}

func (h *writeHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if h.tmp == nil {
		return fuse.ReadResultData(nil), 0
	}
	nread, err := h.tmp.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:nread]), 0
}

// commit uploads the staged bytes and (re)writes the file's triples via
// client.WriteFile, which versions any superseded content and no-ops when the
// bytes are unchanged (identical hash). It is idempotent via h.committed. The
// caller must hold h.mu and guarantee h.tmp != nil.
func (h *writeHandle) commit() syscall.Errno {
	if h.committed {
		return 0
	}
	if err := h.tmp.Sync(); err != nil {
		return syscall.EIO
	}
	n := h.node
	parentUID, err := n.state.tie.DirUIDFromPath(n.parentPath)
	if err != nil {
		return syscall.EIO
	}
	if parentUID == "" {
		if parentUID, err = n.state.tie.MkTieDirAll(client.FileURIScheme + n.parentPath); err != nil {
			return syscall.EIO
		}
	}
	newHash, err := n.state.tie.WriteFile(n.state.host, "", parentUID, n.name, h.tmpPath, nil)
	if err != nil {
		return syscall.EIO
	}
	h.committed = true
	n.file.Uid = newHash
	if fi, err := h.tmp.Stat(); err == nil {
		n.file.Size = int(fi.Size())
	}
	return 0
}

// Flush commits on close(2), but only once a write has actually landed (dirty).
// The kernel can deliver FLUSH *before* WRITE for a truncating open (e.g. shell
// `echo >file`), so an unconditional commit here would upload an empty file and
// then ignore the real bytes. Committing only when dirty keeps the common
// write-then-close path synchronous with close (so a read right after close sees
// the content); the reordered case and an intentionally-empty file are handled
// by Release, the guaranteed-final op.
func (h *writeHandle) Flush(ctx context.Context) syscall.Errno {
	if h.tmp == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.dirty {
		return 0
	}
	return h.commit()
}

// Release is the last op on the handle: the kernel sends it only after every
// WRITE and FLUSH has completed, so it is the safe place to commit a write that
// FLUSH could not (a write reordered after flush, or an empty file that never
// received one). Then it drops the staging temp file.
func (h *writeHandle) Release(ctx context.Context) syscall.Errno {
	if h.tmp == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	errno := h.commit()
	h.tmp.Close()
	os.Remove(h.tmpPath)
	h.tmp = nil
	return errno
}

// Fsync is a no-op success: Flush commits, and the triplestore owns durability
// (mirrors metaFileHandle.Fsync).
func (h *writeHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return 0
}

// suffixName inserts a "~<short>" disambiguator into name, before the final
// extension if there is one, using the first 8 characters of subject (a DirUID or
// content hash). "SomeDir" -> "SomeDir~42e55dcf"; "a.jpg" -> "a~0f4098fb.jpg".
func suffixName(name, subject string) string {
	short := subject
	if len(short) > 8 {
		short = short[:8]
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return base + "~" + short + ext
}

func (d *tagQueryDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	files, err := d.query()
	if err != nil {
		return nil, syscall.EIO
	}
	return taggedFileStream(d.state, files), 0
}

func (d *tagQueryDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	files, err := d.query()
	if err != nil {
		return nil, syscall.EIO
	}
	return lookupTaggedFile(ctx, &d.Inode, d.state, files, name, out)
}

// staticFile serves fixed in-memory bytes (e.g. the /query README).
type staticFile struct {
	fs.Inode
	data []byte
}

var (
	_ = (fs.NodeGetattrer)((*staticFile)(nil))
	_ = (fs.NodeOpener)((*staticFile)(nil))
	_ = (fs.NodeReader)((*staticFile)(nil))
)

func (f *staticFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Size = uint64(len(f.data))
	out.Mode = 0444 // Readonly regular file
	// staticFile doesn't have access to state, but it's only used for the README
	// which is a synthetic file. Leave it as root:root since it's truly virtual.
	return 0
}

func (f *staticFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_RDWR|syscall.O_WRONLY) != 0 {
		return nil, 0, syscall.EROFS
	}
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (f *staticFile) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	return readAt(f.data, dest, off)
}

// staticFileHandle serves a fixed byte snapshot for the duration of one open,
// used for files whose contents are fetched live at Open time (e.g. the tag
// list).
type staticFileHandle struct {
	data []byte
}

var _ = (fs.FileReader)((*staticFileHandle)(nil))

func (h *staticFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	return readAt(h.data, dest, off)
}

// readAt copies the [off, off+len(dest)) window of data into a ReadResult,
// clamping to the end of data.
func readAt(data, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off < 0 || off > int64(len(data)) {
		return nil, syscall.EINVAL
	}
	end := off + int64(len(dest))
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return fuse.ReadResultData(data[off:end]), 0
}

// taggedFileStream builds a readdir stream from a set of tagged files, reusing
// the content-addressed inode numbering.
func taggedFileStream(state *TieDBFuse, files []client.TaggedFile) fs.DirStream {
	entries := make([]fuse.DirEntry, 0, len(files))
	for _, f := range files {
		mode := uint32(fuse.S_IFREG)
		if f.IsDir || f.IsArchive {
			mode = fuse.S_IFDIR
		}
		entries = append(entries, fuse.DirEntry{
			Name: f.Filename,
			Mode: mode,
			Ino:  state.fuseTree.inodeID(f.Hash),
		})
	}
	return fs.NewListDirStream(entries)
}

// lookupTaggedFile resolves one filename within a set of tagged files. A file
// result becomes a content-addressed node serving its bytes; a directory result's
// hash is a DirUID, so it expands live via ReadTieDir (a taggedDir), reflecting
// the directory's current contents rather than an import-time snapshot.
func lookupTaggedFile(ctx context.Context, parent *fs.Inode, state *TieDBFuse, files []client.TaggedFile, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	for _, f := range files {
		if f.Filename != name {
			continue
		}
		if f.IsDir {
			child := &taggedDir{state: state, uid: client.DirUID(f.Hash)}
			stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: state.fuseTree.inodeID(f.Hash)}
			return parent.NewInode(ctx, child, stable), 0
		}
		if f.IsArchive {
			child := &archiveDir{state: state, hash: f.Hash}
			stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: state.fuseTree.inodeID(f.Hash)}
			return parent.NewInode(ctx, child, stable), 0
		}
		child := &node{Hash: f.Hash, Size: f.Size, Mode: fuse.S_IFREG, State: state.fuseTree}
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: state.fuseTree.inodeID(f.Hash)}
		out.Size = uint64(f.Size)
		return parent.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}

// taggedDir expands a directory that appeared in a tag/type query result. It is
// keyed on the directory's DirUID and lists the DirUID's current children via
// ReadTieDir — the same live path-tree navigation used by /files and /tags — so
// entering a query-result directory reflects its present contents. Subdirectories
// recurse as taggedDirs; files resolve to content-addressed nodes.
type taggedDir struct {
	fs.Inode
	state *TieDBFuse
	uid   client.DirUID
}

var (
	_ = (fs.NodeReaddirer)((*taggedDir)(nil))
	_ = (fs.NodeLookuper)((*taggedDir)(nil))
)

func (d *taggedDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	dir, err := client.ReadTieDir(d.state.tie, d.uid)
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(dir.SubDirs)+len(dir.Files)+len(dir.Archives))
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := baseName(p); name != "" {
				entries = append(entries, fuse.DirEntry{
					Name: name,
					Mode: fuse.S_IFDIR,
					Ino:  d.state.fuseTree.inodeID(string(sub.Uid)),
				})
			}
		}
	}
	for _, a := range dir.Archives {
		entries = append(entries, fuse.DirEntry{
			Name: a.Filename,
			Mode: fuse.S_IFDIR,
			Ino:  d.state.fuseTree.inodeID(a.Hash),
		})
	}
	for _, f := range disambiguateFiles(dir.Files) {
		entries = append(entries, fuse.DirEntry{
			Name: f.Filename,
			Mode: fuse.S_IFREG,
			Ino:  d.state.fuseTree.inodeID(f.Uid),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *taggedDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	dir, err := client.ReadTieDir(d.state.tie, d.uid)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if baseName(p) != name {
				continue
			}
			child := &taggedDir{state: d.state, uid: sub.Uid}
			stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: d.state.fuseTree.inodeID(string(sub.Uid))}
			return d.NewInode(ctx, child, stable), 0
		}
	}
	for _, a := range dir.Archives {
		if a.Filename != name {
			continue
		}
		child := &archiveDir{state: d.state, hash: a.Hash}
		stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: d.state.fuseTree.inodeID(a.Hash)}
		return d.NewInode(ctx, child, stable), 0
	}
	for _, f := range disambiguateFiles(dir.Files) {
		if f.Filename != name {
			continue
		}
		child := fileNode(d.state, f)
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.fuseTree.inodeID(f.Uid)}
		out.Size = uint64(f.Size)
		return d.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}

// pathDir exposes the path-based virtual directory tree (the tie:/... hierarchy
// built by import/MkTieDir). Each node resolves its slash path to a DirUID and
// lists that directory's children live on every readdir.
type pathDir struct {
	fs.Inode
	state *TieDBFuse
	path  string // slash path relative to the tie root, e.g. "/" or "/music"
}

var (
	_ = (fs.NodeReaddirer)((*pathDir)(nil))
	_ = (fs.NodeLookuper)((*pathDir)(nil))
	_ = (fs.NodeRenamer)((*pathDir)(nil))
	_ = (fs.NodeMkdirer)((*pathDir)(nil))
	_ = (fs.NodeCreater)((*pathDir)(nil))
)

// renameNoReplace is the renameat2 RENAME_NOREPLACE flag (fail if the
// destination already exists). Go's syscall package doesn't export it.
const renameNoReplace = 0x1

func (d *pathDir) read() (client.Directory, error) {
	uid, err := d.state.tie.DirUIDFromPath(d.path)
	if err != nil {
		return client.Directory{}, err
	}
	if uid == "" {
		return client.Directory{}, syscall.ENOENT
	}
	return client.ReadTieDir(d.state.tie, uid)
}

// baseName returns the last segment of a tie:/-prefixed virtual path.
func baseName(p string) string {
	p = strings.TrimPrefix(p, client.FileURIScheme)
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (d *pathDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	dir, err := d.read()
	if errors.Is(err, syscall.ENOENT) {
		// A path dir with no triples yet (e.g. the /files root on a fresh store)
		// is a real, writable directory that simply holds nothing — list it empty
		// rather than failing, so it can be populated with mkdir/create.
		return fs.NewListDirStream(nil), 0
	}
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(dir.SubDirs)+len(dir.Files)+len(dir.Archives))
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := baseName(p); name != "" {
				entries = append(entries, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR})
			}
		}
	}
	for _, a := range dir.Archives {
		entries = append(entries, fuse.DirEntry{
			Name: a.Filename,
			Mode: fuse.S_IFDIR,
			Ino:  d.state.fuseTree.inodeID(a.Hash),
		})
	}
	for _, f := range disambiguateFiles(dir.Files) {
		entries = append(entries, fuse.DirEntry{
			Name: f.Filename,
			Mode: fuse.S_IFREG,
			Ino:  d.state.fuseTree.inodeID(f.Uid),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *pathDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	dir, err := d.read()
	if errors.Is(err, syscall.ENOENT) {
		return nil, syscall.ENOENT // empty/absent dir has no children (yet)
	}
	if err != nil {
		return nil, syscall.EIO
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if baseName(p) != name {
				continue
			}
			child := &pathDir{state: d.state, path: childPath(d.path, name)}
			return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
		}
	}
	for _, a := range dir.Archives {
		if a.Filename != name {
			continue
		}
		child := &archiveDir{state: d.state, hash: a.Hash}
		stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: d.state.fuseTree.inodeID(a.Hash)}
		return d.NewInode(ctx, child, stable), 0
	}
	for _, f := range disambiguateFiles(dir.Files) {
		if f.Filename != name {
			continue
		}
		child := &dbFileNode{state: d.state, parentPath: d.path, name: name, file: f}
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.fuseTree.inodeID(f.Uid)}
		out.Size = uint64(f.Size)
		return d.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}

// Create makes a new file in this directory: it stages an empty temp file and
// returns a writable handle. The content is uploaded and its triples written on
// close (see writeHandle.Flush), so the real content hash — and the file's
// inode identity — is only known after the bytes are written. Copying onto an
// existing name is allowed: the overwrite is versioned into <name>_prev on
// commit (client.WriteFile), so there is no EEXIST here.
func (d *pathDir) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	child := &dbFileNode{state: d.state, parentPath: d.path, name: name}
	wh, err := child.newWriteHandle()
	if err != nil {
		return nil, nil, 0, syscall.EIO
	}
	stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.fuseTree.inodeID(d.path + "\x00" + name)}
	inode := d.NewInode(ctx, child, stable)
	out.Mode = 0644 | fuse.S_IFREG
	out.Uid = d.state.uid
	out.Gid = d.state.gid
	return inode, wh, fuse.FOPEN_DIRECT_IO, 0
}

// Mkdir creates a subdirectory of this directory in the path tree, writing the
// directory's triples (parent/path/tie-type) to the store via MkTieDirAll. The
// new directory is materialized as a pathDir inode so it can be listed and
// further populated immediately. MkTieDirAll (not MkTieDir) is used so the "/"
// root — and any not-yet-materialized ancestor — is created too; on a fresh
// store the root does not exist yet, and MkTieDir would parent the new dir under
// an empty root UID, orphaning it from the /files view.
func (d *pathDir) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	tie := d.state.tie
	childSlash := childPath(d.path, name)
	if existing, err := tie.DirUIDFromPath(childSlash); err == nil && existing != "" {
		return nil, syscall.EEXIST
	}
	if _, err := tie.MkTieDirAll(client.FileURIScheme + childSlash); err != nil {
		return nil, syscall.EIO
	}
	if err := tie.Sync(); err != nil {
		return nil, syscall.EIO
	}
	child := &pathDir{state: d.state, path: childSlash}
	return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
}

// Rename moves a child of this directory (name) to newParent under newName,
// writing the change back to the triple store. Only moves within the /files
// path tree are supported: a destination in another tree (e.g. /query) returns
// EXDEV. RENAME_EXCHANGE is unsupported. RENAME_NOREPLACE (and, in practice,
// any collision with an existing directory) returns EEXIST.
func (d *pathDir) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if flags&fs.RENAME_EXCHANGE != 0 {
		return syscall.ENOSYS
	}
	dstDir, ok := newParent.(*pathDir)
	if !ok {
		return syscall.EXDEV
	}
	tie := d.state.tie

	srcParent, err := tie.DirUIDFromPath(d.path)
	if err != nil || srcParent == "" {
		return syscall.EIO
	}
	dstParent, err := tie.DirUIDFromPath(dstDir.path)
	if err != nil || dstParent == "" {
		return syscall.EIO
	}

	dir, err := d.read()
	if err != nil {
		return syscall.EIO
	}

	// Locate the source entry: a file (subject = content hash) or a subdir.
	for _, f := range dir.Files {
		if f.Filename != name {
			continue
		}
		if flags&renameNoReplace != 0 && d.destExists(dstDir, newName) {
			return syscall.EEXIST
		}
		if err := client.RenameFile(tie, f.Uid, srcParent, dstParent, newName); err != nil {
			return syscall.EIO
		}
		return 0
	}
	for _, sub := range dir.SubDirs {
		match := false
		for _, p := range sub.Paths {
			if baseName(p) == name {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		oldPath := client.FileURIScheme + childPath(d.path, name)
		newPath := client.FileURIScheme + childPath(dstDir.path, newName)
		// Reject collisions with an existing destination directory.
		if existing, err := tie.DirUIDFromPath(childPath(dstDir.path, newName)); err == nil && existing != "" {
			return syscall.EEXIST
		}
		if err := client.RenameDir(tie, sub.Uid, oldPath, newPath, srcParent, dstParent); err != nil {
			return syscall.EIO
		}
		return 0
	}
	return syscall.ENOENT
}

// destExists reports whether dstDir already has a child (file or subdir) named
// newName. Used to honor RENAME_NOREPLACE.
func (d *pathDir) destExists(dstDir *pathDir, newName string) bool {
	dir, err := dstDir.read()
	if err != nil {
		return false
	}
	for _, f := range dir.Files {
		if f.Filename == newName {
			return true
		}
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if baseName(p) == newName {
				return true
			}
		}
	}
	return false
}

// childPath joins a parent slash path and a child segment, avoiding a double
// slash when the parent is the root "/".
func childPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

// tagsDir mirrors the pathDir navigation of the /files tree, but its leaf files
// are tagFile nodes: writable text files whose contents are the tags attached to
// the underlying content. Each directory also exposes a ".tags" file bound to
// the directory's own DirUID, so a directory's tags are editable the same way a
// leaf file's are.
type tagsDir struct {
	fs.Inode
	state *TieDBFuse
	path  string // slash path relative to the tie root, e.g. "/" or "/music"
}

// tagsSelfName is the fixed filename inside every /tags directory that exposes
// the directory's own tags for editing. It is a tagFile bound to the directory's
// DirUID rather than a content hash. A real child file literally named ".tags"
// would be shadowed by it, which is why a dotfile name is used.
const tagsSelfName = ".tags"

// tagsTypeName is the fixed filename inside every /tags directory that exposes
// the directory's own tie-type classification labels for editing. It is a
// metaFile bound to the directory's DirUID (via newTypeFile). Like .tags it uses
// a dotfile name so a real child file named ".type" isn't shadowed.
const tagsTypeName = ".type"

// typeInodeKey derives the inode key for a directory's .type file. It must differ
// from the .tags key (the bare DirUID, used for the directory's own tag file) so
// the two control files in one directory get distinct inodes.
func typeInodeKey(uid client.DirUID) string { return string(uid) + "\x00type" }

var (
	_ = (fs.NodeReaddirer)((*tagsDir)(nil))
	_ = (fs.NodeLookuper)((*tagsDir)(nil))
)

func (d *tagsDir) read() (client.Directory, error) {
	uid, err := d.state.tie.DirUIDFromPath(d.path)
	if err != nil {
		return client.Directory{}, err
	}
	if uid == "" {
		return client.Directory{}, syscall.ENOENT
	}
	return client.ReadTieDir(d.state.tie, uid)
}

func (d *tagsDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	dir, err := d.read()
	if errors.Is(err, syscall.ENOENT) {
		return fs.NewListDirStream(nil), 0 // empty/absent path dir: nothing to aggregate
	}
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(dir.SubDirs)+len(dir.Files)+2)
	entries = append(entries,
		fuse.DirEntry{
			Name: tagsSelfName,
			Mode: fuse.S_IFREG,
			Ino:  d.state.fuseTree.inodeID(string(dir.Uid)),
		},
		fuse.DirEntry{
			Name: tagsTypeName,
			Mode: fuse.S_IFREG,
			Ino:  d.state.fuseTree.inodeID(typeInodeKey(dir.Uid)),
		},
	)
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := baseName(p); name != "" {
				entries = append(entries, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR})
			}
		}
	}
	for _, f := range disambiguateFiles(dir.Files) {
		entries = append(entries, fuse.DirEntry{
			Name: f.Filename,
			Mode: fuse.S_IFREG,
			Ino:  d.state.fuseTree.inodeID(f.Uid),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *tagsDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	dir, err := d.read()
	if errors.Is(err, syscall.ENOENT) {
		return nil, syscall.ENOENT // empty/absent dir has no children (yet)
	}
	if err != nil {
		return nil, syscall.EIO
	}
	if name == tagsSelfName {
		child := newTagFile(d.state, string(dir.Uid))
		out.Size = uint64(len(child.render()))
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.fuseTree.inodeID(string(dir.Uid))}
		return d.NewInode(ctx, child, stable), 0
	}
	if name == tagsTypeName {
		child := newTypeFile(d.state, dir.Uid)
		out.Size = uint64(len(child.render()))
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.fuseTree.inodeID(typeInodeKey(dir.Uid))}
		return d.NewInode(ctx, child, stable), 0
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if baseName(p) != name {
				continue
			}
			child := &tagsDir{state: d.state, path: childPath(d.path, name)}
			return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
		}
	}
	for _, f := range disambiguateFiles(dir.Files) {
		if f.Filename != name {
			continue
		}
		child := newTagFile(d.state, f.Uid)
		out.Size = uint64(len(child.render()))
		return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	}
	return nil, syscall.ENOENT
}

// metaFile is a writable text file whose contents are a set of string values,
// one per line, sorted. Reading it snapshots the current set; writing it replaces
// the set: editors truncate-then-rewrite, so the file buffers written bytes and,
// on flush/close, parses the buffer into a set and commits it via store. The
// empty buffer clears the set. load/store bind one metaFile to a concrete backing
// set — a content hash's tags (newTagFile) or a directory's tie-type labels
// (newTypeFile) — so the two share this one truncate-safe write path.
type metaFile struct {
	fs.Inode
	state *TieDBFuse
	load  func() ([]string, error)
	store func([]string) error
}

var (
	_ = (fs.NodeGetattrer)((*metaFile)(nil))
	_ = (fs.NodeSetattrer)((*metaFile)(nil))
	_ = (fs.NodeOpener)((*metaFile)(nil))
)

// newTagFile builds a metaFile backing one content hash's tags.
func newTagFile(state *TieDBFuse, hash string) *metaFile {
	return &metaFile{
		state: state,
		load:  func() ([]string, error) { return client.GetTags(state.tie, hash) },
		store: func(v []string) error { return client.SetTags(state.tie, hash, v) },
	}
}

// newTypeFile builds a metaFile backing one directory's tie-type classification
// labels (the structural "directory" marker is preserved by SetDirTypes and never
// surfaced by GetDirType, so editing this file can't break navigation).
func newTypeFile(state *TieDBFuse, uid client.DirUID) *metaFile {
	return &metaFile{
		state: state,
		load:  func() ([]string, error) { return client.GetDirType(state.tie, uid) },
		store: func(v []string) error { return client.SetDirTypes(state.tie, uid, v) },
	}
}

// render returns the current values as newline-terminated bytes. Errors surface
// as empty content so a stat/read never panics; the write path reports real
// errors.
func (f *metaFile) render() []byte {
	vals, err := f.load()
	if err != nil || len(vals) == 0 {
		return nil
	}
	return []byte(strings.Join(vals, "\n") + "\n")
}

func (f *metaFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Size = uint64(len(f.render()))
	out.Mode = 0644 // Writable regular file
	out.Uid = f.state.uid
	out.Gid = f.state.gid
	return 0
}

// Setattr accepts node-level attribute changes. The one that matters is the
// truncate the kernel issues before a rewrite open (go-fuse does not advertise
// ATOMIC_O_TRUNC, so truncation arrives here as a SETATTR(size) rather than an
// O_TRUNC open flag). A writable open rebuilds the set from scratch, so this
// only needs to acknowledge the change; other attributes are accepted as no-ops
// so chmod/utimes from tools don't fail.
func (f *metaFile) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		out.Size = sz
		return 0
	}
	out.Size = uint64(len(f.render()))
	return 0
}

// Open returns a metaFileHandle. A read-only open snapshots the current values so
// a cat shows them. A writable open starts from an empty buffer and commits the
// buffer verbatim on flush: a meta file is always fully rewritten (shell
// redirection, tee, editor save), so the written bytes are the new set.
func (f *metaFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	h := &metaFileHandle{file: f}
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		h.writable = true
		h.buf = []byte{}
	} else {
		h.buf = f.render()
	}
	return h, fuse.FOPEN_DIRECT_IO, 0
}

// metaFileHandle carries the per-open buffer of text. Direct I/O is used so the
// kernel doesn't cache a stale size between the truncate and the rewrite.
type metaFileHandle struct {
	file     *metaFile
	buf      []byte
	writable bool
}

var (
	_ = (fs.FileReader)((*metaFileHandle)(nil))
	_ = (fs.FileWriter)((*metaFileHandle)(nil))
	_ = (fs.FileFlusher)((*metaFileHandle)(nil))
	_ = (fs.FileFsyncer)((*metaFileHandle)(nil))
)

func (h *metaFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	return readAt(h.buf, dest, off)
}

func (h *metaFileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if !h.writable {
		return 0, syscall.EBADF
	}
	end := off + int64(len(data))
	if int64(len(h.buf)) < end {
		grown := make([]byte, end)
		copy(grown, h.buf)
		h.buf = grown
	}
	copy(h.buf[off:end], data)
	return uint32(len(data)), 0
}

// Flush commits the buffered text on close: split into lines, each line one
// value, and replace the backing set via store. Any writable open commits, even
// if no bytes were written, so emptying the file (e.g. ": > file") clears the set.
func (h *metaFileHandle) Flush(ctx context.Context) syscall.Errno {
	if !h.writable {
		return 0
	}
	if err := h.file.store(parseLines(h.buf)); err != nil {
		return syscall.EIO
	}
	return 0
}

// Fsync is called by editors (e.g. vim) to ensure data is persisted. Since Flush
// already commits the buffered text to the triple store, Fsync is a no-op: the
// backing store (tie-triplestore) handles its own durability. Returning success lets
// editors save without E667: Fsync failed errors.
func (h *metaFileHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return 0
}

// parseLines splits meta-file contents into individual values: one value per
// line, surrounding whitespace trimmed, blank lines dropped.
func parseLines(data []byte) []string {
	var vals []string
	for _, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			vals = append(vals, t)
		}
	}
	return vals
}

// MountDB mounts the tag-derived virtual filesystem at mountpoint.
func (state *TieDBFuse) MountDB(mountpoint string) (*fuse.Server, error) {
	root := &dbRoot{state: state}
	return fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{Debug: false},
	})
}
