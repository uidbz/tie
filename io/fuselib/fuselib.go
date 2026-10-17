package fuselib

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"syscall"
	"time"

	"github.com/h2non/filetype"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/io/fhclient"
	"github.com/uidbz/tie/io/tiefs"
	"github.com/uidbz/tie/metadata"
)

// TieFuse is the go-fuse adapter for a content-addressed tree. The shared
// read cache, inode numbering, and write staging live in tiefs.Store; this
// layer only translates between go-fuse node operations and those primitives.
type TieFuse struct {
	store  *tiefs.Store
	config config
}

// Close releases the on-disk blob cache. Call once after the mount is torn down.
func (state *TieFuse) Close() error {
	return state.store.Close()
}

// NewTieFuse builds a content-addressed FUSE state. verify controls whether
// downloaded blob bytes are hashed and checked against their content address:
// off by default, since for a trusted personal filehost the per-read hash pass
// over multi-GB media is wasted work; turn it on when the filehost is untrusted.
func NewTieFuse(host client.FileHost, cacheSizeGB int, verify bool) *TieFuse {
	return &TieFuse{
		store:  tiefs.NewStore(host, cacheSizeGB, verify),
		config: config{fh: fhclient.New(host.URL, client.HTTPClientFor(host)), verify: verify},
	}
}

type config struct {
	// fh is the filehost client shared by every content read of the mount.
	fh     *fhclient.Client
	verify bool
}

func (state *TieFuse) inodeID(hash string) uint64 { return state.store.InodeID(hash) }

func (state *TieFuse) cache() *tiefs.Cache { return state.store.Cache() }

// Ensure we are implementing the NodeReaddirer interface
var _ = (fs.NodeReaddirer)((*node)(nil))

// Readdir is part of the NodeReaddirer interface
func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	n, err := n.State.listContent(n.Hash)
	if err != nil {
		return nil, syscall.ENOENT
	}
	r := make([]fuse.DirEntry, 0, len(n.Children))

	for _, x := range n.Children {
		d := fuse.DirEntry{
			Name: x.Name,
			Ino:  n.State.inodeID(x.inodeKey()),
			Mode: x.Mode,
		}
		r = append(r, d)
	}
	return fs.NewListDirStream(r), 0
}

// Ensure we are implementing the NodeLookuper interface
var _ = (fs.NodeLookuper)((*node)(nil))

// Lookup is part of the NodeLookuper interface
func (current *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n, err := current.State.listContent(current.Hash)
	if err != nil {
		return nil, syscall.ENOENT
	}
	var child *node
	for _, x := range n.Children {
		if x.Name == name {
			child = x
			break
		}
	}
	if child == nil {
		return nil, syscall.ENOENT
	}

	stable := fs.StableAttr{
		Mode: child.Mode,
		// The child inode is identified by its Inode number.
		// If multiple concurrent lookups try to find the same
		// inode, they are deduplicated on this key.
		Ino: current.State.inodeID(child.inodeKey()),
	}
	operations := &node{
		Hash:          child.Hash,
		Size:          child.Size,
		State:         current.State,
		ArchiveHash:   child.ArchiveHash,
		ArchiveMember: child.ArchiveMember,
	} // The Open function is run on this

	// fmt.Println("OP:", operations.Hash, operations.Size)
	// The NewInode call wraps the `operations` object into an Inode.
	c := current.NewInode(ctx, operations, stable)

	// In case of concurrent lookup requests, it can happen that operations !=
	// child.Operations().
	return c, 0
}

var _ = (fs.NodeOpener)((*node)(nil))

// Getattr sets the minimum, which is the size. A more full-featured
// FS would also set timestamps and permissions.
var _ = (fs.NodeGetattrer)((*node)(nil))

func (n *node) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Size = uint64(n.Size)
	// Content-addressed nodes are immutable: directories get 0555 (r-xr-xr-x),
	// files get 0444 (r--r--r--).
	if n.Mode&fuse.S_IFDIR != 0 {
		out.Mode = 0555 | fuse.S_IFDIR
	} else {
		out.Mode = 0444 | fuse.S_IFREG
	}
	out.Uid = n.State.store.UID()
	out.Gid = n.State.store.GID()
	if !n.Mtime.IsZero() {
		out.SetTimes(nil, &n.Mtime, &n.Mtime)
	}
	return 0
}

func (f *node) Open(ctx context.Context, openFlags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	// disallow writes
	if fuseFlags&(syscall.O_RDWR|syscall.O_WRONLY) != 0 {
		return nil, 0, syscall.EROFS
	}

	if f.ArchiveMember != "" {
		return &archiveMemberFileHandle{
			cache:       f.State.cache(),
			archiveHash: f.ArchiveHash,
			member:      f.ArchiveMember,
		}, fuse.FOPEN_KEEP_CACHE, fs.OK
	}

	fh = &bytesFileHandle{
		hash:  f.Hash,
		cache: f.State.cache(),
	}

	// Return FOPEN_DIRECT_IO if content should not be cached.
	// return fh, fuse.FOPEN_DIRECT_IO, fs.OK
	return fh, fuse.FOPEN_KEEP_CACHE, fs.OK
}

// bytesFileHandle is a file handle that serves one blob's bytes from the cache.
type bytesFileHandle struct {
	hash  string
	cache *tiefs.Cache
}

// Read serves a clamped window of the blob: FUSE reads past the end return the
// remaining bytes without an error, unlike io.ReaderAt.
var _ = (fs.FileReader)((*bytesFileHandle)(nil))

func (fh *bytesFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	e, err := fh.cache.Blob(fh.hash)
	if err != nil {
		return nil, syscall.EIO
	}
	// The handle holds a reference for the duration of the read: eviction can
	// no longer close the fd under us (that was the EIO). Re-resolving per
	// read also keeps the entry hot in the LRU instead of pinning a multi-GB
	// blob for the whole lifetime of an open file.
	defer e.Close()
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

type node struct {
	// Must embed an Inode for the struct to work as a node.
	fs.Inode

	Hash     string
	Name     string
	Mode     uint32  // file (fuse.S_IFREG) or directory (fuse.S_IFDIR)
	Children []*node // for directories
	Size     int
	State    *TieFuse
	// ArchiveHash/ArchiveMember are set on a node that is a member *inside* an
	// archive blob rather than a content-addressed blob of its own. Such a node
	// has an empty Hash; its bytes come from the member cache keyed on the
	// (archive hash, member name) pair.
	ArchiveHash   string
	ArchiveMember string
	// Mtime is the file's modification time, surfaced from its import date
	// (tag-date). Zero leaves the mount's default (unset) timestamp.
	Mtime time.Time
}

func (n *node) AddChild(child *node) {
	n.Children = append(n.Children, child)
}

// inodeKey is the stable identity used to deduplicate inodes. A normal node is
// keyed by its content hash; an archive member has no hash of its own, so it is
// keyed by its (archive hash, member name) pair.
func (n *node) inodeKey() string {
	if n.ArchiveMember != "" {
		return tiefs.MemberKey(n.ArchiveHash, n.ArchiveMember)
	}
	return n.Hash
}

const dirHeader = metadata.DirHeader

func (state *TieFuse) listContent(sourceHash string) (*node, error) {
	// fhclient validates the content address and the status, so a crafted
	// hash never reaches a URL and a non-200 is an error rather than an empty
	// directory.
	rc, err := state.config.fh.Get(sourceHash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	// When verification is enabled, check the blob against its hash before
	// trusting its bytes (the filehost is untrusted). Off by default.
	if state.config.verify {
		got, err := metadata.HashReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if got != sourceHash {
			return nil, errors.New("fuselib: content does not match its hash")
		}
	}

	n := &node{
		Hash:  sourceHash,
		State: state,
	}

	isDir := len(body) >= len(dirHeader) && string(body[:len(dirHeader)]) == dirHeader
	if isDir {
		n.Mode = fuse.S_IFDIR
		scanner := bufio.NewScanner(bytes.NewReader(body))
		for scanner.Scan() {
			entry, ok := metadata.ParseDirLine(scanner.Text())
			if !ok {
				continue
			}
			child := &node{
				Hash:  entry.Hash,
				Name:  entry.Filename,
				Size:  entry.Size,
				State: state,
			}
			if metadata.IsDirHead(entry.Head) {
				child.Mode = fuse.S_IFDIR
			} else {
				child.Mode = fuse.S_IFREG
			}
			n.AddChild(child)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	} else if filetype.IsArchive(body) {
		// A zip blob browsed by hash expands into a virtual directory of its
		// members. The blob has no tie-type triples here, so detection is by
		// magic bytes. Members are served from the member cache on read.
		n.Mode = fuse.S_IFDIR
		members, err := archivelib.List(bytes.NewReader(body))
		if err != nil {
			// Not a readable archive after all — fall back to a plain file.
			n.Mode = fuse.S_IFREG
			return n, nil
		}
		// Flatten nested member paths to disambiguated base names — a dirent name
		// cannot contain '/'. The full member name is kept as ArchiveMember for
		// extraction. Shared with the --db mount's archiveDir.
		for _, v := range tiefs.ArchiveMemberViews(members) {
			child := &node{
				Name:          v.Display(),
				Size:          int(v.Member().Size),
				Mode:          fuse.S_IFREG,
				State:         state,
				ArchiveHash:   sourceHash,
				ArchiveMember: v.Member().Name,
			}
			n.AddChild(child)
		}
	} else {
		n.Mode = fuse.S_IFREG
	}

	return n, nil
}

func (state *TieFuse) Mount(hash, mountpoint string) (*fuse.Server, error) {
	root, err := state.listContent(hash)
	if err != nil {
		return nil, err
	}

	server, err := fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			// Set to true to see how the file system works.
			Debug: false,
		},
	})
	if err != nil {
		return nil, err
	}

	return server, nil
}
