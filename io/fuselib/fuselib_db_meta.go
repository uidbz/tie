package fuselib

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"
)

// metaFile is a writable text file whose contents are a set of string values,
// one per line, sorted. Reading it snapshots the current set; writing it replaces
// the set: editors truncate-then-rewrite, so the file buffers written bytes and,
// on flush/close, parses the buffer into a set and commits it via the tiesfs
// MetaBinding (a content hash's tags or a directory's tie-type labels). The
// empty buffer clears the set.
type metaFile struct {
	fs.Inode
	state *TieDBFuse
	bind  tiesfs.MetaBinding
}

var (
	_ = (fs.NodeGetattrer)((*metaFile)(nil))
	_ = (fs.NodeSetattrer)((*metaFile)(nil))
	_ = (fs.NodeOpener)((*metaFile)(nil))
)

// newTagFile builds a metaFile backing one content hash's tags.
func newTagFile(state *TieDBFuse, hash string) *metaFile {
	return &metaFile{state: state, bind: state.tree.TagsBinding(hash)}
}

// newTypeFile builds a metaFile backing one directory's tie-type classification
// labels (the structural "directory" marker is preserved by SetDirTypes and never
// surfaced by GetDirType, so editing this file can't break navigation).
func newTypeFile(state *TieDBFuse, uid client.DirUID) *metaFile {
	return &metaFile{state: state, bind: state.tree.DirTypesBinding(uid)}
}

// render returns the current values as newline-terminated bytes. Errors surface
// as empty content so a stat/read never panics; the write path reports real
// errors.
func (f *metaFile) render() []byte {
	vals, err := f.bind.Load()
	if err != nil {
		return nil
	}
	return tiesfs.RenderMeta(vals)
}

func (f *metaFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Size = uint64(len(f.render()))
	out.Mode = 0644 // Writable regular file
	out.Uid = f.state.tree.UID()
	out.Gid = f.state.tree.GID()
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
// value, and replace the backing set via Store. Any writable open commits, even
// if no bytes were written, so emptying the file (e.g. ": > file") clears the set.
func (h *metaFileHandle) Flush(ctx context.Context) syscall.Errno {
	if !h.writable {
		return 0
	}
	if err := h.file.bind.Store(tiesfs.ParseLines(h.buf)); err != nil {
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
