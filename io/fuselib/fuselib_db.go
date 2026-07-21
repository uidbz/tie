package fuselib

import (
	"context"
	"strings"
	"syscall"

	"git.sr.ht/~uid/tie/client"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// TieDBFuse serves a live, mutable virtual filesystem derived from the triple
// store. The layout is:
//
//	/tag                       a text file explaining the query syntax
//	/tag/<query>/<filename>    files matching a tag query; the directory name is
//	                           the query itself, e.g. "jazz mellow -live" ANDs
//	                           jazz and mellow, excludes live. A "type:audio"
//	                           token scopes to a media type (default: all types).
//	/by-tag/<tag>/<filename>   flat listing of the files carrying one tag
//	/files/<path>/...          the path-based virtual directory tree (file:/...)
//
// Files serve their bytes from the filehost by hash; tagged directories expand
// as immutable content-addressed trees (tiedir blobs). Unlike the
// content-addressed Mount, this reflects the store as-is on every readdir:
// re-tagging is visible without remounting.
type TieDBFuse struct {
	tie      *client.TieClient
	fuseTree *TieFuse // reused for content-addressed file/dir bytes + cache
}

func NewTieDBFuse(tie *client.TieClient, filehost string, insecure bool, cacheSizeGB int) *TieDBFuse {
	return &TieDBFuse{
		tie:      tie,
		fuseTree: NewTieFuse(filehost, insecure, cacheSizeGB),
	}
}

const tagHelpText = `tie tag-query filesystem
=========================

Navigate to a directory whose name is a tag query to list the matching files:

    ls "tag/jazz"                 files tagged jazz
    ls "tag/jazz mellow"          tagged jazz AND mellow
    ls "tag/jazz mellow -live"    tagged jazz AND mellow, but NOT live

Space separates terms (all ANDed). A leading "-" excludes a tag.

Scope to a media type with a "type:" token (default: all types):

    ls "tag/mellow type:audio"    audio files tagged mellow
    ls "tag/type:image"           all image files

Other top-level directories:

    by-tag/<tag>/    flat listing of the files carrying one tag
    files/<path>/    the path-based virtual directory tree
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
		{Name: "tag", Mode: fuse.S_IFDIR},
		{Name: "by-tag", Mode: fuse.S_IFDIR},
		{Name: "files", Mode: fuse.S_IFDIR},
	}
	return fs.NewListDirStream(entries), 0
}

func (r *dbRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case "tag":
		child := &tagQueryRoot{state: r.state}
		return r.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
	case "by-tag":
		child := &byTagRoot{state: r.state}
		return r.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
	case "files":
		child := &pathDir{state: r.state, path: "/"}
		return r.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
	}
	return nil, syscall.ENOENT
}

// tagQueryRoot is the /tag directory. It cannot enumerate every possible query,
// so a readdir shows only a README explaining the syntax; each lookup of a
// non-README name is treated as a query string and resolved live.
type tagQueryRoot struct {
	fs.Inode
	state *TieDBFuse
}

var (
	_ = (fs.NodeReaddirer)((*tagQueryRoot)(nil))
	_ = (fs.NodeLookuper)((*tagQueryRoot)(nil))
)

const tagHelpName = "README"

func (q *tagQueryRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{{Name: tagHelpName, Mode: fuse.S_IFREG}}
	return fs.NewListDirStream(entries), 0
}

func (q *tagQueryRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name == tagHelpName {
		child := &staticFile{data: []byte(tagHelpText)}
		out.Size = uint64(len(child.data))
		return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	}
	// Treat the directory name as a tag query; validate it resolves before
	// materializing the inode so a bogus query returns ENOENT rather than an
	// empty directory.
	mediaType, include, exclude := parseTagQuery(name)
	if len(include) == 0 && mediaType == client.TieUnknownFile {
		return nil, syscall.ENOENT
	}
	child := &tagQueryDir{state: q.state, mediaType: mediaType, include: include, exclude: exclude}
	return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
}

// mediaTypeToken maps the friendly "type:" token values to TieTypes. It also
// accepts the full stringer names (e.g. "audio-file") via StringToTieType below.
var mediaTypeToken = map[string]client.TieType{
	"image":    client.TieImageFile,
	"audio":    client.TieAudioFile,
	"video":    client.TieVideoFile,
	"document": client.TieDocumentFile,
	"archive":  client.TieArchiveFile,
}

// parseTagQuery splits a query directory name into a media-type scope and the
// include/exclude tag sets. Space separates terms; a leading "-" excludes; a
// "type:<name>" token sets the scope (default: TieUnknownFile, meaning all
// types). <name> accepts a friendly form ("audio") or the full type name
// ("audio-file"). The convention matches the `tie get` CLI.
func parseTagQuery(query string) (mediaType client.TieType, include, exclude []string) {
	mediaType = client.TieUnknownFile
	for _, term := range strings.Fields(query) {
		switch {
		case strings.HasPrefix(term, "type:"):
			name := strings.TrimPrefix(term, "type:")
			if t, ok := mediaTypeToken[name]; ok {
				mediaType = t
			} else {
				mediaType = client.StringToTieType(name)
			}
		case strings.HasPrefix(term, "-"):
			if tag := term[1:]; tag != "" {
				exclude = append(exclude, tag)
			}
		default:
			include = append(include, term)
		}
	}
	return mediaType, include, exclude
}

// tagQueryDir lists the files matching one tag query.
type tagQueryDir struct {
	fs.Inode
	state     *TieDBFuse
	mediaType client.TieType
	include   []string
	exclude   []string
}

var (
	_ = (fs.NodeReaddirer)((*tagQueryDir)(nil))
	_ = (fs.NodeLookuper)((*tagQueryDir)(nil))
)

func (d *tagQueryDir) query() ([]client.TaggedFile, error) {
	files, _, err := d.state.tie.FilesWithTags(d.mediaType, d.include, d.exclude, 0, 0)
	return files, err
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

// staticFile serves fixed in-memory bytes (e.g. the /tag README).
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
	return 0
}

func (f *staticFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&(syscall.O_RDWR|syscall.O_WRONLY) != 0 {
		return nil, 0, syscall.EROFS
	}
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

func (f *staticFile) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off < 0 || off > int64(len(f.data)) {
		return nil, syscall.EINVAL
	}
	end := off + int64(len(dest))
	if end > int64(len(f.data)) {
		end = int64(len(f.data))
	}
	return fuse.ReadResultData(f.data[off:end]), 0
}

// taggedFileStream builds a readdir stream from a set of tagged files, reusing
// the content-addressed inode numbering.
func taggedFileStream(state *TieDBFuse, files []client.TaggedFile) fs.DirStream {
	entries := make([]fuse.DirEntry, 0, len(files))
	for _, f := range files {
		mode := uint32(fuse.S_IFREG)
		if f.IsDir {
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

// lookupTaggedFile resolves one filename within a set of tagged files to a
// content-addressed node (a file serves its bytes, a tagged directory expands as
// an immutable tiedir tree).
func lookupTaggedFile(ctx context.Context, parent *fs.Inode, state *TieDBFuse, files []client.TaggedFile, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	for _, f := range files {
		if f.Filename != name {
			continue
		}
		mode := uint32(fuse.S_IFREG)
		if f.IsDir {
			mode = fuse.S_IFDIR
		}
		child := &node{Hash: f.Hash, Size: f.Size, Mode: mode, State: state.fuseTree}
		stable := fs.StableAttr{Mode: mode, Ino: state.fuseTree.inodeID(f.Hash)}
		out.Size = uint64(f.Size)
		return parent.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}

// byTagRoot lists one directory per known tag.
type byTagRoot struct {
	fs.Inode
	state *TieDBFuse
}

var (
	_ = (fs.NodeReaddirer)((*byTagRoot)(nil))
	_ = (fs.NodeLookuper)((*byTagRoot)(nil))
)

func (b *byTagRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	tags, _, err := b.state.tie.ListTags(0, 0)
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(tags))
	for _, tag := range tags {
		entries = append(entries, fuse.DirEntry{Name: tag, Mode: fuse.S_IFDIR})
	}
	return fs.NewListDirStream(entries), 0
}

func (b *byTagRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	child := &tagDir{state: b.state, tag: name}
	return b.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
}

// tagDir lists the files (and tagged sub-directories) carrying one tag.
type tagDir struct {
	fs.Inode
	state *TieDBFuse
	tag   string
}

var (
	_ = (fs.NodeReaddirer)((*tagDir)(nil))
	_ = (fs.NodeLookuper)((*tagDir)(nil))
)

func (d *tagDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	files, _, err := d.state.tie.FilesWithTag(d.tag, 0, 0)
	if err != nil {
		return nil, syscall.EIO
	}
	return taggedFileStream(d.state, files), 0
}

func (d *tagDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	files, _, err := d.state.tie.FilesWithTag(d.tag, 0, 0)
	if err != nil {
		return nil, syscall.EIO
	}
	return lookupTaggedFile(ctx, &d.Inode, d.state, files, name, out)
}

// pathDir exposes the path-based virtual directory tree (the file:/... hierarchy
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
)

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

// baseName returns the last segment of a file:/-prefixed virtual path.
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
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(dir.SubDirs)+len(dir.Files))
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := baseName(p); name != "" {
				entries = append(entries, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR})
			}
		}
	}
	for _, f := range dir.Files {
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
	for _, f := range dir.Files {
		if f.Filename != name {
			continue
		}
		child := &node{Hash: f.Uid, Size: f.Size, Mode: fuse.S_IFREG, State: d.state.fuseTree}
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.fuseTree.inodeID(f.Uid)}
		out.Size = uint64(f.Size)
		return d.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}

// childPath joins a parent slash path and a child segment, avoiding a double
// slash when the parent is the root "/".
func childPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

// MountDB mounts the tag-derived virtual filesystem at mountpoint.
func (state *TieDBFuse) MountDB(mountpoint string) (*fuse.Server, error) {
	root := &dbRoot{state: state}
	return fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{Debug: false},
	})
}
