package fuselib

import (
	"context"
	"errors"
	stdfs "io/fs"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"
)

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
	return d.state.tree.QueryFiles(d.scope, d.include, d.exclude)
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

// taggedDir expands a directory that appeared in a tag/type query result. It is
// keyed on the directory's DirUID and lists the DirUID's current children via
// ReadTieDir - the same live path-tree navigation used by /files and /tags - so
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
	dir, err := client.ReadTieDir(d.state.tree.Tie, d.uid)
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(dir.SubDirs)+len(dir.Files)+len(dir.Archives))
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := tiesfs.BaseName(p); name != "" {
				entries = append(entries, fuse.DirEntry{
					Name: name,
					Mode: fuse.S_IFDIR,
					Ino:  d.state.tree.InodeID(string(sub.Uid)),
				})
			}
		}
	}
	for _, a := range dir.Archives {
		entries = append(entries, fuse.DirEntry{
			Name: a.Filename,
			Mode: fuse.S_IFDIR,
			Ino:  d.state.tree.InodeID(a.Hash),
		})
	}
	for _, f := range tiesfs.DisambiguateFiles(dir.Files) {
		entries = append(entries, fuse.DirEntry{
			Name: f.Filename,
			Mode: fuse.S_IFREG,
			Ino:  d.state.tree.InodeID(f.Uid),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *taggedDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	dir, err := client.ReadTieDir(d.state.tree.Tie, d.uid)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if tiesfs.BaseName(p) != name {
				continue
			}
			child := &taggedDir{state: d.state, uid: sub.Uid}
			stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: d.state.tree.InodeID(string(sub.Uid))}
			return d.NewInode(ctx, child, stable), 0
		}
	}
	for _, a := range dir.Archives {
		if a.Filename != name {
			continue
		}
		child := &archiveDir{state: d.state, hash: a.Hash}
		stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: d.state.tree.InodeID(a.Hash)}
		return d.NewInode(ctx, child, stable), 0
	}
	for _, f := range tiesfs.DisambiguateFiles(dir.Files) {
		if f.Filename != name {
			continue
		}
		child := fileNode(d.state, f)
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.tree.InodeID(f.Uid)}
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
	_ = (fs.NodeUnlinker)((*pathDir)(nil))
	_ = (fs.NodeRmdirer)((*pathDir)(nil))
)

// renameNoReplace is the renameat2 RENAME_NOREPLACE flag (fail if the
// destination already exists). Go's syscall package doesn't export it.
const renameNoReplace = 0x1

func (d *pathDir) read() (client.Directory, error) {
	return d.state.tree.PathDir(d.path)
}

func (d *pathDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	dir, err := d.read()
	if errors.Is(err, stdfs.ErrNotExist) {
		// A path dir with no triples yet (e.g. the /files root on a fresh store)
		// is a real, writable directory that simply holds nothing - list it empty
		// rather than failing, so it can be populated with mkdir/create.
		return fs.NewListDirStream(nil), 0
	}
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(dir.SubDirs)+len(dir.Files)+len(dir.Archives))
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := tiesfs.BaseName(p); name != "" {
				entries = append(entries, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR})
			}
		}
	}
	for _, a := range dir.Archives {
		entries = append(entries, fuse.DirEntry{
			Name: a.Filename,
			Mode: fuse.S_IFDIR,
			Ino:  d.state.tree.InodeID(a.Hash),
		})
	}
	for _, f := range tiesfs.DisambiguateFiles(dir.Files) {
		entries = append(entries, fuse.DirEntry{
			Name: f.Filename,
			Mode: fuse.S_IFREG,
			Ino:  d.state.tree.InodeID(f.Uid),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *pathDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	dir, err := d.read()
	if errors.Is(err, stdfs.ErrNotExist) {
		return nil, syscall.ENOENT // empty/absent dir has no children (yet)
	}
	if err != nil {
		return nil, syscall.EIO
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if tiesfs.BaseName(p) != name {
				continue
			}
			child := &pathDir{state: d.state, path: tiesfs.ChildPath(d.path, name)}
			return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
		}
	}
	for _, a := range dir.Archives {
		if a.Filename != name {
			continue
		}
		child := &archiveDir{state: d.state, hash: a.Hash}
		stable := fs.StableAttr{Mode: fuse.S_IFDIR, Ino: d.state.tree.InodeID(a.Hash)}
		return d.NewInode(ctx, child, stable), 0
	}
	for _, f := range tiesfs.DisambiguateFiles(dir.Files) {
		if f.Filename != name {
			continue
		}
		child := &dbFileNode{state: d.state, parentPath: d.path, name: name, file: f}
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.tree.InodeID(f.Uid)}
		out.Size = uint64(f.Size)
		return d.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}

// Create makes a new file in this directory: it stages an empty temp file and
// returns a writable handle. The content is uploaded and its triples written on
// close (see writeHandle.Flush), so the real content hash - and the file's
// inode identity - is only known after the bytes are written. Copying onto an
// existing name is allowed: the overwrite is versioned into <name>_prev on
// commit (client.WriteFile), so there is no EEXIST here.
func (d *pathDir) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	child := &dbFileNode{state: d.state, parentPath: d.path, name: name}
	wh, err := child.newWriteHandle()
	if err != nil {
		return nil, nil, 0, syscall.EIO
	}
	stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.tree.InodeID(d.path + "\x00" + name)}
	inode := d.NewInode(ctx, child, stable)
	out.Mode = 0644 | fuse.S_IFREG
	out.Uid = d.state.tree.UID()
	out.Gid = d.state.tree.GID()
	return inode, wh, fuse.FOPEN_DIRECT_IO, 0
}

// Mkdir creates a subdirectory of this directory in the path tree, writing the
// directory's triples (parent/path/tie-type) to the store via MkTieDirAll. The
// new directory is materialized as a pathDir inode so it can be listed and
// further populated immediately.
func (d *pathDir) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if _, err := d.state.tree.Mkdir(d.path, name); err != nil {
		return nil, errnoFor(err, syscall.EIO)
	}
	child := &pathDir{state: d.state, path: tiesfs.ChildPath(d.path, name)}
	return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
}

// Unlink removes a file child from the path tree: the (hash,"parent") edge is
// dropped, and the blob's lifetime stays with the filehost's retention policy.
// Shared content (parented in another directory) keeps its metadata and stays
// visible there.
func (d *pathDir) Unlink(ctx context.Context, name string) syscall.Errno {
	err := d.state.tree.DeleteFile(d.path, name)
	return errnoFor(err, syscall.EIO)
}

// Rmdir removes an empty subdirectory. A non-empty directory returns ENOTEMPTY.
func (d *pathDir) Rmdir(ctx context.Context, name string) syscall.Errno {
	err := d.state.tree.DeleteDir(d.path, name)
	if errors.Is(err, tiesfs.ErrNotEmpty) {
		return syscall.ENOTEMPTY
	}
	return errnoFor(err, syscall.EIO)
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
	replace := flags&renameNoReplace == 0
	err := d.state.tree.Rename(d.path, name, dstDir.path, newName, replace)
	return errnoFor(err, syscall.EIO)
}
