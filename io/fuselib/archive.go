package fuselib

import (
	"context"
	"io"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/io/tiefs"
)

// archiveMemberFileHandle serves one archive member's decompressed bytes from
// the member cache.
type archiveMemberFileHandle struct {
	cache       *tiefs.Cache
	archiveHash string
	member      string
}

var _ = (fs.FileReader)((*archiveMemberFileHandle)(nil))

func (fh *archiveMemberFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	e, err := fh.cache.Member(fh.archiveHash, fh.member)
	if err != nil {
		return nil, syscall.EIO
	}
	defer e.Close() // hold the reference only for the read; see bytesFileHandle
	size := e.Size()
	if off < 0 || off > size {
		return nil, syscall.EINVAL
	}
	end := off + int64(len(dest))
	if end > size {
		end = size
	}
	n, err := e.ReadAt(dest[:end-off], off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

// archiveDir presents an archive blob (a zip) as a live directory of its
// members in the --db mount. It is keyed on the archive's content hash; members
// are listed by expanding the blob and served through the member cache. Nested
// member paths are flattened to their base name (an image viewer wants the
// pictures, not the internal folder layout); base-name collisions are
// disambiguated deterministically (tiefs.ArchiveMemberViews).
type archiveDir struct {
	fs.Inode
	state *TieDBFuse
	hash  string
}

var (
	_ = (fs.NodeReaddirer)((*archiveDir)(nil))
	_ = (fs.NodeLookuper)((*archiveDir)(nil))
)

func (d *archiveDir) members() ([]archivelib.Member, error) {
	return d.state.tree.Cache().ArchiveMembers(d.hash)
}

func (d *archiveDir) memberNode(m archivelib.Member) *node {
	return &node{
		Size:          int(m.Size),
		Mode:          fuse.S_IFREG,
		State:         d.state.fuseTree,
		ArchiveHash:   d.hash,
		ArchiveMember: m.Name,
	}
}

func (d *archiveDir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	members, err := d.members()
	if err != nil {
		return nil, syscall.EIO
	}
	views := tiefs.ArchiveMemberViews(members)
	entries := make([]fuse.DirEntry, 0, len(views))
	for _, v := range views {
		entries = append(entries, fuse.DirEntry{
			Name: v.Display(),
			Mode: fuse.S_IFREG,
			Ino:  d.state.tree.InodeID(tiefs.MemberKey(d.hash, v.Member().Name)),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *archiveDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	members, err := d.members()
	if err != nil {
		return nil, syscall.EIO
	}
	for _, v := range tiefs.ArchiveMemberViews(members) {
		if v.Display() != name {
			continue
		}
		child := d.memberNode(v.Member())
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.tree.InodeID(tiefs.MemberKey(d.hash, v.Member().Name))}
		out.Size = uint64(v.Member().Size)
		return d.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}
