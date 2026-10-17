package fuselib

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"
)

// dbFileNode is a writable file in the /files path tree. Reads serve the stored
// content by hash from the shared blob cache (identical to the read-only path);
// a writable open stages bytes to a temp file and, on close, uploads them and
// rewrites the file's triples via tiefs.WriteHandle (which uses
// client.WriteFile, versioning any prior content into <name>_prev). file.Uid is
// empty for a not-yet-committed Create.
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
	h, err := n.state.tree.NewWriteHandle(n.parentPath, n.name, n.file)
	if err != nil {
		return nil, err
	}
	return &writeHandle{node: n, h: h}, nil
}

func (n *dbFileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0644 | fuse.S_IFREG
	out.Uid = n.state.tree.UID()
	out.Gid = n.state.tree.GID()
	if wh, ok := fh.(*writeHandle); ok && wh.h != nil {
		out.Size = uint64(wh.h.Size())
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
		if wh, ok := fh.(*writeHandle); ok && wh.h != nil {
			if err := wh.h.Truncate(int64(sz)); err != nil {
				return syscall.EIO
			}
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
		return &bytesFileHandle{hash: n.file.Uid, cache: n.state.tree.Cache()}, fuse.FOPEN_KEEP_CACHE, 0
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
		if err := wh.h.Seed(); err != nil {
			wh.h.Close()
			return nil, 0, syscall.EIO
		}
	}
	return wh, fuse.FOPEN_DIRECT_IO, 0
}

// writeHandle adapts a tiefs.WriteHandle to go-fuse's file-handle operations.
type writeHandle struct {
	node *dbFileNode
	h    *tiefs.WriteHandle // nil for the read handle of an uncommitted Create
}

var (
	_ = (fs.FileWriter)((*writeHandle)(nil))
	_ = (fs.FileReader)((*writeHandle)(nil))
	_ = (fs.FileFlusher)((*writeHandle)(nil))
	_ = (fs.FileReleaser)((*writeHandle)(nil))
	_ = (fs.FileFsyncer)((*writeHandle)(nil))
)

func (h *writeHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if h.h == nil {
		return 0, syscall.EBADF
	}
	n, err := h.h.WriteAt(data, off)
	if err != nil {
		return uint32(n), syscall.EIO
	}
	return uint32(n), 0
}

func (h *writeHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if h.h == nil {
		return fuse.ReadResultData(nil), 0
	}
	size := h.h.Size()
	if off >= size {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > size {
		end = size
	}
	n, err := h.h.ReadAt(dest[:end-off], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

// Flush commits on close(2), but only once a write has actually landed (dirty).
// The kernel can deliver FLUSH *before* WRITE for a truncating open (e.g. shell
// `echo >file`), so an unconditional commit here would upload an empty file and
// then ignore the real bytes. Committing only when dirty keeps the common
// write-then-close path synchronous with close (so a read right after close sees
// the content); the reordered case and an intentionally-empty file are handled
// by Release, the guaranteed-final op.
func (h *writeHandle) Flush(ctx context.Context) syscall.Errno {
	if h.h == nil {
		return 0
	}
	if !h.h.Dirty() {
		return 0
	}
	if err := h.h.Commit(); err != nil {
		return syscall.EIO
	}
	h.node.file = h.h.File()
	return 0
}

// Release is the last op on the handle: the kernel sends it only after every
// WRITE and FLUSH has completed, so it is the safe place to commit a write that
// FLUSH could not (a write reordered after flush, or an empty file that never
// received one). Then it drops the staging temp file.
func (h *writeHandle) Release(ctx context.Context) syscall.Errno {
	if h.h == nil {
		return 0
	}
	err := h.h.Close()
	h.node.file = h.h.File()
	if err != nil {
		return syscall.EIO
	}
	return 0
}

// Fsync is a no-op success: Flush commits, and the triplestore owns durability
// (mirrors metaFileHandle.Fsync).
func (h *writeHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return 0
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

var (
	_ = (fs.NodeReaddirer)((*tagsDir)(nil))
	_ = (fs.NodeLookuper)((*tagsDir)(nil))
)

func (d *tagsDir) read() (client.Directory, error) {
	return d.state.tree.PathDir(d.path)
}

func (d *tagsDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	dir, err := d.read()
	if errors.Is(err, stdfs.ErrNotExist) {
		return fs.NewListDirStream(nil), 0 // empty/absent path dir: nothing to aggregate
	}
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(dir.SubDirs)+len(dir.Files)+2)
	entries = append(entries,
		fuse.DirEntry{
			Name: tiefs.TagsSelfName,
			Mode: fuse.S_IFREG,
			Ino:  d.state.tree.InodeID(string(dir.Uid)),
		},
		fuse.DirEntry{
			Name: tiefs.TagsTypeName,
			Mode: fuse.S_IFREG,
			Ino:  d.state.tree.InodeID(tiefs.TypeInodeKey(dir.Uid)),
		},
	)
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := tiefs.BaseName(p); name != "" {
				entries = append(entries, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR})
			}
		}
	}
	for _, f := range tiefs.DisambiguateFiles(dir.Files) {
		entries = append(entries, fuse.DirEntry{
			Name: f.Filename,
			Mode: fuse.S_IFREG,
			Ino:  d.state.tree.InodeID(f.Uid),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *tagsDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	dir, err := d.read()
	if errors.Is(err, stdfs.ErrNotExist) {
		return nil, syscall.ENOENT // empty/absent dir has no children (yet)
	}
	if err != nil {
		return nil, syscall.EIO
	}
	if name == tiefs.TagsSelfName {
		child := newTagFile(d.state, string(dir.Uid))
		out.Size = uint64(len(child.render()))
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.tree.InodeID(string(dir.Uid))}
		return d.NewInode(ctx, child, stable), 0
	}
	if name == tiefs.TagsTypeName {
		child := newTypeFile(d.state, dir.Uid)
		out.Size = uint64(len(child.render()))
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.tree.InodeID(tiefs.TypeInodeKey(dir.Uid))}
		return d.NewInode(ctx, child, stable), 0
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if tiefs.BaseName(p) != name {
				continue
			}
			child := &tagsDir{state: d.state, path: tiefs.ChildPath(d.path, name)}
			return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
		}
	}
	for _, f := range tiefs.DisambiguateFiles(dir.Files) {
		if f.Filename != name {
			continue
		}
		child := newTagFile(d.state, f.Uid)
		out.Size = uint64(len(child.render()))
		return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG}), 0
	}
	return nil, syscall.ENOENT
}
