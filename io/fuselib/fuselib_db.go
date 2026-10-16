package fuselib

import (
	"context"
	"errors"
	stdfs "io/fs"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/fhclient"
	"github.com/uidbz/tie/io/tiefs"
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
//
// All tree semantics (query parsing, disambiguation, path navigation, staged
// writes, meta files) live in tiesfs; this file is the go-fuse adapter.
type TieDBFuse struct {
	tree     *tiesfs.Tree
	fuseTree *TieFuse // reused for content-addressed file/dir bytes + cache
}

func NewTieDBFuse(tie *client.TieClient, host client.FileHost, cacheSizeGB int, verify bool) *TieDBFuse {
	store := tiesfs.NewStore(host, cacheSizeGB, verify)
	return &TieDBFuse{
		tree:     tiesfs.NewTreeWithStore(store, tie),
		fuseTree: &TieFuse{store: store, config: config{fh: fhclient.New(host.URL, client.HTTPClientFor(host)), verify: verify}},
	}
}

// Close releases the on-disk blob cache and the write-staging dir. Call once
// after the mount is torn down.
func (state *TieDBFuse) Close() error {
	return state.tree.Close()
}

// errnoFor maps a tiesfs io/fs error onto the FUSE errno the kernel expects.
// A nil error maps to 0 (success).
func errnoFor(err error, fallback syscall.Errno) syscall.Errno {
	if err == nil {
		return 0
	}
	switch {
	case errors.Is(err, stdfs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, stdfs.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, stdfs.ErrPermission):
		return syscall.EACCES
	default:
		return fallback
	}
}

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

func (q *tagQueryRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{
		{Name: tiesfs.ReadmeName, Mode: fuse.S_IFREG},
		{Name: tiesfs.TagListName, Mode: fuse.S_IFREG},
		{Name: tiesfs.TypeListName, Mode: fuse.S_IFREG},
	}
	// Saved queries from config appear as ready-made query directories.
	for name := range q.state.tree.Tie.Config.Queries {
		entries = append(entries, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR})
	}
	return fs.NewListDirStream(entries), 0
}

func (q *tagQueryRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	switch name {
	case tiesfs.ReadmeName:
		child := &staticFile{data: []byte(tiesfs.ReadmeText)}
		out.Size = uint64(len(child.data))
		return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	case tiesfs.TagListName:
		child := &tagListFile{state: q.state}
		return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	case tiesfs.TypeListName:
		child := &typeListFile{state: q.state}
		return q.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	}
	// A saved query name resolves to its stored expression; otherwise the name
	// is itself treated as an ad-hoc query.
	query := name
	if saved, ok := q.state.tree.SavedQuery(name); ok {
		query = saved
	}
	// Validate the query resolves before materializing the inode so a bogus
	// query returns ENOENT rather than an empty directory.
	scope, include, exclude := tiesfs.ParseTagQuery(query)
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
	tags, err := f.state.tree.TagList()
	if err != nil {
		return nil, syscall.EIO
	}
	return tiesfs.RenderMeta(tags), 0
}

func (f *tagListFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	data, errno := f.contents()
	if errno != 0 {
		return errno
	}
	out.Size = uint64(len(data))
	out.Mode = 0444 // Readonly regular file
	out.Uid = f.state.tree.UID()
	out.Gid = f.state.tree.GID()
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
	types, err := f.state.tree.TypeList()
	if err != nil {
		return nil, syscall.EIO
	}
	return tiesfs.RenderMeta(types), 0
}

func (f *typeListFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	data, errno := f.contents()
	if errno != 0 {
		return errno
	}
	out.Size = uint64(len(data))
	out.Mode = 0444 // Readonly regular file
	out.Uid = f.state.tree.UID()
	out.Gid = f.state.tree.GID()
	return 0
}

// Open snapshots the current type list into a read-only handle, so the bytes
// stay consistent for the duration of one open.
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
			Ino:  state.tree.InodeID(f.Hash),
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
			stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: state.tree.InodeID(f.Hash)}
			return parent.NewInode(ctx, child, stable), 0
		}
		if f.IsArchive {
			child := &archiveDir{state: state, hash: f.Hash}
			stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: state.tree.InodeID(f.Hash)}
			return parent.NewInode(ctx, child, stable), 0
		}
		child := &node{Hash: f.Hash, Size: f.Size, Mode: fuse.S_IFREG, State: state.fuseTree}
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: state.tree.InodeID(f.Hash)}
		out.Size = uint64(f.Size)
		return parent.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
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

// MountDB mounts the tag-derived virtual filesystem at mountpoint.
func (state *TieDBFuse) MountDB(mountpoint string) (*fuse.Server, error) {
	root := &dbRoot{state: state}
	return fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{Debug: false},
	})
}
