package fuselib

import (
	"context"
	"sort"
	"syscall"

	"git.sr.ht/~uid/tie/client"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// TieDBFuse serves a live, mutable virtual filesystem derived from the triple
// store's tags. The layout is:
//
//	/by-tag/<tag>/<filename>   file bytes fetched from the filehost by hash
//	/by-tag/<tag>/<dir>/...    a tagged directory, expanded as an immutable
//	                           content-addressed tree (tiedir blob)
//
// Unlike the content-addressed Mount, this reflects the store as-is on every
// readdir: re-tagging is visible without remounting.
type TieDBFuse struct {
	tie      *client.TieClient
	fuseTree *TieFuse // reused for content-addressed file/dir bytes + cache
}

func NewTieDBFuse(tie *client.TieClient, filehost string, cacheSizeGB int) *TieDBFuse {
	return &TieDBFuse{
		tie:      tie,
		fuseTree: NewTieFuse(filehost, cacheSizeGB),
	}
}

// dbRoot lists the top-level virtual directories (currently just /by-tag).
type dbRoot struct {
	fs.Inode
	state *TieDBFuse
}

var (
	_ = (fs.NodeReaddirer)((*dbRoot)(nil))
	_ = (fs.NodeLookuper)((*dbRoot)(nil))
)

func (r *dbRoot) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries := []fuse.DirEntry{{Name: "by-tag", Mode: fuse.S_IFDIR}}
	return fs.NewListDirStream(entries), 0
}

func (r *dbRoot) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if name != "by-tag" {
		return nil, syscall.ENOENT
	}
	child := &byTagRoot{state: r.state}
	return r.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR}), 0
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
	tags, err := b.state.tie.ListTags()
	if err != nil {
		return nil, syscall.EIO
	}
	sort.Strings(tags)
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
	files, err := d.state.tie.FilesWithTag(d.tag)
	if err != nil {
		return nil, syscall.EIO
	}
	entries := make([]fuse.DirEntry, 0, len(files))
	for _, f := range files {
		mode := uint32(fuse.S_IFREG)
		if f.IsDir {
			mode = fuse.S_IFDIR
		}
		entries = append(entries, fuse.DirEntry{
			Name: f.Filename,
			Mode: mode,
			Ino:  d.state.fuseTree.inodeID(f.Hash),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *tagDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	files, err := d.state.tie.FilesWithTag(d.tag)
	if err != nil {
		return nil, syscall.EIO
	}
	for _, f := range files {
		if f.Filename != name {
			continue
		}
		// Reuse the content-addressed node: a plain file serves its bytes, a
		// tagged directory expands as an immutable tiedir tree.
		mode := uint32(fuse.S_IFREG)
		if f.IsDir {
			mode = fuse.S_IFDIR
		}
		child := &node{Hash: f.Hash, Size: f.Size, Mode: mode, State: d.state.fuseTree}
		stable := fs.StableAttr{Mode: mode, Ino: d.state.fuseTree.inodeID(f.Hash)}
		out.Size = uint64(f.Size)
		return d.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}

// MountDB mounts the tag-derived virtual filesystem at mountpoint.
func (state *TieDBFuse) MountDB(mountpoint string) (*fuse.Server, error) {
	root := &dbRoot{state: state}
	return fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{Debug: false},
	})
}
