package fuselib

import (
	"context"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"syscall"

	"github.com/uidbz/tie/io/archivelib"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// memberKey is the cache key for one extracted archive member. The archive hash
// is hex and the member name cannot contain a NUL, so the join is unambiguous.
func memberKey(archiveHash, member string) string {
	return archiveHash + "\x00" + member
}

// archiveMembers downloads the archive blob into the cache (once, single-flight)
// and lists its members from the seekable cached file.
func (c *cache) archiveMembers(archiveHash string) ([]archivelib.Member, error) {
	ae, err := c.blob(archiveHash)
	if err != nil {
		return nil, err
	}
	// A SectionReader gives an independent, zero-based view of the shared cache
	// fd, so concurrent listings/reads don't race on its offset.
	return archivelib.List(io.NewSectionReader(ae.file, 0, ae.size))
}

// memberFile extracts one archive member into its own cache entry and returns an
// open, read-only handle to it. Zip members may be deflate-compressed, which is
// not seekable, so the member is decompressed to a cache file up front; reads
// then use random-access ReadAt like any other blob. Bounded by disk, not RAM.
func (c *cache) memberFile(archiveHash, member string) (*cacheEntry, error) {
	return c.get(memberKey(archiveHash, member), func() (*os.File, int64, error) {
		ae, err := c.blob(archiveHash)
		if err != nil {
			return nil, 0, err
		}
		rc, err := archivelib.Open(io.NewSectionReader(ae.file, 0, ae.size), member)
		if err != nil {
			return nil, 0, err
		}
		defer rc.Close()

		f, err := os.CreateTemp(c.dir, "member-")
		if err != nil {
			return nil, 0, err
		}
		path := f.Name()
		size, err := io.Copy(f, rc)
		if err != nil {
			f.Close()
			os.Remove(path)
			return nil, 0, err
		}
		// Unlink now: the open fd keeps the bytes reachable; the dir is removed
		// wholesale on cache close.
		os.Remove(path)
		return f, size, nil
	})
}

// archiveMemberFileHandle serves one archive member's decompressed bytes from
// the member cache.
type archiveMemberFileHandle struct {
	cache       *cache
	archiveHash string
	member      string
}

var _ = (fs.FileReader)((*archiveMemberFileHandle)(nil))

func (fh *archiveMemberFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	e, err := fh.cache.memberFile(fh.archiveHash, fh.member)
	if err != nil {
		return nil, syscall.EIO
	}
	if off < 0 || off > e.size {
		return nil, syscall.EINVAL
	}
	end := off + int64(len(dest))
	if end > e.size {
		end = e.size
	}
	n, err := e.file.ReadAt(dest[:end-off], off)
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
// disambiguated deterministically.
type archiveDir struct {
	fs.Inode
	state *TieDBFuse
	hash  string
}

var (
	_ = (fs.NodeReaddirer)((*archiveDir)(nil))
	_ = (fs.NodeLookuper)((*archiveDir)(nil))
)

// archiveMemberView pairs a member with its flattened display name.
type archiveMemberView struct {
	display string
	member  archivelib.Member
}

// archiveMemberViews maps members to disambiguated base-name display entries.
// It sorts by full member name first so Readdir and Lookup agree on every call.
func archiveMemberViews(members []archivelib.Member) []archiveMemberView {
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	views := make([]archiveMemberView, 0, len(members))
	seen := make(map[string]int)
	for _, m := range members {
		base := path.Base(m.Name)
		if base == "" || base == "." || base == "/" {
			continue
		}
		n := seen[base]
		seen[base]++
		display := base
		if n > 0 {
			display = suffixName(base, strconv.Itoa(n))
		}
		views = append(views, archiveMemberView{display: display, member: m})
	}
	return views
}

func (d *archiveDir) members() ([]archivelib.Member, error) {
	return d.state.fuseTree.cache.archiveMembers(d.hash)
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
	views := archiveMemberViews(members)
	entries := make([]fuse.DirEntry, 0, len(views))
	for _, v := range views {
		entries = append(entries, fuse.DirEntry{
			Name: v.display,
			Mode: fuse.S_IFREG,
			Ino:  d.state.fuseTree.inodeID(memberKey(d.hash, v.member.Name)),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (d *archiveDir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	members, err := d.members()
	if err != nil {
		return nil, syscall.EIO
	}
	for _, v := range archiveMemberViews(members) {
		if v.display != name {
			continue
		}
		child := d.memberNode(v.member)
		stable := fs.StableAttr{Mode: fuse.S_IFREG, Ino: d.state.fuseTree.inodeID(memberKey(d.hash, v.member.Name))}
		out.Size = uint64(v.member.Size)
		return d.NewInode(ctx, child, stable), 0
	}
	return nil, syscall.ENOENT
}
