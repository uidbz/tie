// Package smbfs exposes the tie live tag-derived filesystem (the same tree
// `tie mount --db` serves: /query, /files, /tags) as a
// github.com/go-filesystems/interface Filesystem for the SMB server.
//
// It is the SMB twin of io/fuselib's TieDBFuse: all tree semantics (query
// parsing, disambiguation, path navigation, staged writes, meta files) live
// in tiefs; this package adapts them to the path-based Filesystem + Opener
// + WritableFile contract.
//
// # Paths
//
// The smb server hands over cleaned slash paths ("/a/b", never "..", never
// ":", segments as the client typed them). tie names are arbitrary, so
// display names are escaped (see escape.go) and segments are unescaped here
// before resolution. tie names are case-sensitive; the smb library's
// case-folded retry (resolveCase) only kicks in when the exact name misses.
//
// # Errors
//
// io/fs conventions throughout: missing paths wrap fs.ErrNotExist,
// collisions fs.ErrExist, writes to read-only views fs.ErrPermission. The
// smb server maps those onto SMB status codes.
package smbfs

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	gofs "github.com/go-filesystems/interface"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"
)

// Top-level view names, mirroring the --db mount.
const (
	viewQuery = "query"
	viewFiles = "files"
	viewTags  = "tags"
)

// Share is one SMB export of the tie tree. It implements
// filesystem.Filesystem and filesystem.Opener, and OpenFile returns
// filesystem.WritableFile handles for the writable views.
type Share struct {
	tree *tiefs.Tree

	mu      sync.Mutex
	pending map[string]*pendingEntry // canonical path -> staged write
}

// NewShare wraps a tiefs.Tree as an SMB filesystem.
func NewShare(tree *tiefs.Tree) *Share {
	return &Share{tree: tree, pending: make(map[string]*pendingEntry)}
}

// Tree exposes the underlying tree (for tests and the cmd wiring).
func (s *Share) Tree() *tiefs.Tree { return s.tree }

// Close releases the tree (blob cache + staging dir). Open handles commit
// through their own Close; the smb server closes every handle when a client
// connection ends, so nothing is lost on shutdown.
func (s *Share) Close() error { return s.tree.Close() }

var (
	_ = (gofs.Filesystem)((*Share)(nil))
	_ = (gofs.Opener)((*Share)(nil))
)

// nodeKind classifies one resolved path.
type nodeKind int

const (
	kRoot nodeKind = iota
	kQueryRoot
	kQueryDir   // directory whose name is a tag query
	kQueryFile  // file from a query result (content-addressed blob)
	kTaggedDir  // directory that appeared in a query result (live DirUID)
	kArchiveDir // archive blob expanded as a directory of members
	kMember     // one member inside an archive dir
	kPathDir    // /files directory (writable)
	kPathFile   // /files file (writable)
	kTagsDir    // /tags directory (writable meta leaves)
	kMetaFile   // tag/type text file under /tags
	kStatic     // /query/README
	kTagList    // /query/tags
	kTypeList   // /query/types
)

// node is one resolved path: everything Stat/ListDir/OpenFile need to know
// about it.
type node struct {
	kind nodeKind
	// path tree members (kPathDir/kPathFile/kTagsDir/kMetaFile):
	parentSlash string        // tie slash path of the containing dir ("/" for root)
	name        string        // unescaped name within parentSlash
	uid         client.DirUID // kTaggedDir/kTagsDir/kPathDir ("" when unmaterialized)
	hash        string        // kQueryFile/kArchiveDir/kMember/kMetaFile (content hash)
	member      string        // kMember: member name within the archive
	memberSize  int64         // kMember
	query       string        // kQueryDir: the query expression
	file        client.File   // kPathFile: the stored file (zero Uid for pending)
	pending     *pendingEntry
}

// pendingEntry is a staged write registered by WriteFile(p, nil) before the
// server opens the handle (the smb CREATE flow stats the path in between, so
// a pending path must Stat and List as if it existed).
type pendingEntry struct {
	parentSlash string // tie slash path of the containing directory
	name        string // unescaped file name within parentSlash
	meta        bool   // meta buffer handle under /tags vs data handle under /files
	inoKey      string // inode key the committed file will report
	size        func() int64
	h           handle
}

// segments splits a cleaned fs path and unescapes each segment. The result is
// empty for the root.
func segments(p string) []string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	out := parts[:0]
	for _, s := range parts {
		if s == "" {
			continue
		}
		out = append(out, unescapeName(s))
	}
	return out
}

// canonPath re-joins unescaped segments into the canonical (escaped-free)
// form used as the pending-map key, so escaped and unescaped spellings of the
// same path agree.
func canonPath(segs []string) string {
	if len(segs) == 0 {
		return "/"
	}
	return "/" + strings.Join(segs, "/")
}

// resolve maps a client path onto a node. Missing paths return an error
// wrapping fs.ErrNotExist.
func (s *Share) resolve(p string) (*node, error) {
	segs := segments(p)
	if len(segs) == 0 {
		return &node{kind: kRoot}, nil
	}
	switch segs[0] {
	case viewQuery:
		return s.resolveQuery(segs)
	case viewFiles:
		return s.resolvePath(segs[1:], canonPath(segs))
	case viewTags:
		return s.resolveTags(segs[1:], canonPath(segs))
	}
	return nil, fmt.Errorf("smbfs: %q not found: %w", p, fs.ErrNotExist)
}

// resolveQuery resolves /query and its read-only subtree.
func (s *Share) resolveQuery(segs []string) (*node, error) {
	if len(segs) == 1 {
		return &node{kind: kQueryRoot}, nil
	}
	switch segs[1] {
	case tiefs.ReadmeName:
		if len(segs) == 2 {
			return &node{kind: kStatic}, nil
		}
	case tiefs.TagListName:
		if len(segs) == 2 {
			return &node{kind: kTagList}, nil
		}
	case tiefs.TypeListName:
		if len(segs) == 2 {
			return &node{kind: kTypeList}, nil
		}
	}
	// A saved query name resolves to its stored expression; otherwise the
	// name is itself an ad-hoc query (same rule as the --db mount).
	expr, ok := s.tree.SavedQuery(segs[1])
	if !ok {
		expr = segs[1]
	}
	scope, include, exclude := tiefs.ParseTagQuery(expr)
	if scope == "" && len(include) == 0 {
		return nil, fmt.Errorf("smbfs: query %q matches nothing: %w", segs[1], fs.ErrNotExist)
	}
	if len(segs) == 2 {
		return &node{kind: kQueryDir, query: expr}, nil
	}
	files, err := s.tree.QueryFiles(scope, include, exclude)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Filename != segs[2] {
			continue
		}
		switch {
		case f.IsDir:
			return s.descendTagged(client.DirUID(f.Hash), segs[3:], &node{kind: kTaggedDir, uid: client.DirUID(f.Hash)})
		case f.IsArchive:
			return s.descendArchive(f.Hash, segs[3:], &node{kind: kArchiveDir, hash: f.Hash})
		default:
			if len(segs) > 3 {
				break
			}
			return &node{kind: kQueryFile, hash: f.Hash, file: client.File{Uid: f.Hash, Size: f.Size}}, nil
		}
	}
	return nil, fmt.Errorf("smbfs: %q not found: %w", segs[2], fs.ErrNotExist)
}

// descendTagged walks the remaining segments through a live directory
// (ReadTieDir children), starting from a pre-built node for depth 0.
func (s *Share) descendTagged(uid client.DirUID, segs []string, base *node) (*node, error) {
	if len(segs) == 0 {
		return base, nil
	}
	dir, err := client.ReadTieDir(s.tree.Tie, uid)
	if err != nil {
		return nil, err
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if tiefs.BaseName(p) == segs[0] {
				return s.descendTagged(sub.Uid, segs[1:], &node{kind: kTaggedDir, uid: sub.Uid})
			}
		}
	}
	for _, a := range dir.Archives {
		if a.Filename == segs[0] {
			return s.descendArchive(a.Hash, segs[1:], &node{kind: kArchiveDir, hash: a.Hash})
		}
	}
	if len(segs) == 1 {
		for _, f := range tiefs.DisambiguateFiles(dir.Files) {
			if f.Filename == segs[0] {
				return &node{kind: kQueryFile, hash: f.Uid, file: f}, nil
			}
		}
	}
	return nil, fmt.Errorf("smbfs: %q not found: %w", segs[0], fs.ErrNotExist)
}

// descendArchive walks into an archive blob's members.
func (s *Share) descendArchive(hash string, segs []string, base *node) (*node, error) {
	if len(segs) == 0 {
		return base, nil
	}
	if len(segs) != 1 {
		return nil, fmt.Errorf("smbfs: archive %q has no subdirectories: %w", hash, fs.ErrNotExist)
	}
	members, err := s.tree.Cache().ArchiveMembers(hash)
	if err != nil {
		return nil, err
	}
	for _, v := range tiefs.ArchiveMemberViews(members) {
		if v.Display() == segs[0] {
			m := v.Member()
			return &node{kind: kMember, hash: hash, member: m.Name, memberSize: m.Size}, nil
		}
	}
	return nil, fmt.Errorf("smbfs: %q not found in archive: %w", segs[0], fs.ErrNotExist)
}

// resolvePath resolves /files/<slash path>: directories by DirUIDFromPath,
// files and archives by the parent's live listing. Pending creates resolve
// too, so the smb CREATE flow (WriteFile -> Stat -> OpenFile) sees the file
// it just staged.
func (s *Share) resolvePath(segs []string, canon string) (*node, error) {
	if len(segs) == 0 {
		uid, err := s.tree.Tie.DirUIDFromPath("/")
		if err != nil {
			return nil, err
		}
		return &node{kind: kPathDir, parentSlash: "/", uid: uid}, nil
	}
	s.mu.Lock()
	pend := s.pending[canon]
	s.mu.Unlock()
	if pend != nil {
		return &node{kind: kPathFile, parentSlash: pend.parentSlash, name: pend.name, pending: pend}, nil
	}
	parentSlash := "/" + strings.Join(segs[:len(segs)-1], "/")
	name := segs[len(segs)-1]
	childSlash := path.Join(parentSlash, name)

	dir, err := s.tree.PathDir(parentSlash)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("smbfs: %q: %w", parentSlash, err)
		}
		// An unmaterialized parent has no children (yet).
		return nil, fmt.Errorf("smbfs: %q not found: %w", childSlash, fs.ErrNotExist)
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if tiefs.BaseName(p) == name {
				return &node{kind: kPathDir, parentSlash: parentSlash, name: name, uid: sub.Uid}, nil
			}
		}
	}
	for _, a := range dir.Archives {
		if a.Filename == name {
			return &node{kind: kArchiveDir, parentSlash: parentSlash, name: name, hash: a.Hash}, nil
		}
	}
	for _, f := range tiefs.DisambiguateFiles(dir.Files) {
		if f.Filename == name {
			return &node{kind: kPathFile, parentSlash: parentSlash, name: name, hash: f.Uid, file: f}, nil
		}
	}
	return nil, fmt.Errorf("smbfs: %q not found: %w", childSlash, fs.ErrNotExist)
}

// resolveTags resolves /tags/<slash path>: the same path tree as /files, but
// leaf files are meta files and every materialized directory carries
// .tags/.type.
func (s *Share) resolveTags(segs []string, canon string) (*node, error) {
	if len(segs) == 0 {
		uid, err := s.tree.Tie.DirUIDFromPath("/")
		if err != nil {
			return nil, err
		}
		return &node{kind: kTagsDir, parentSlash: "/", uid: uid}, nil
	}
	s.mu.Lock()
	pend := s.pending[canon]
	s.mu.Unlock()
	if pend != nil {
		n, err := s.resolveMeta(canon, segs)
		if err != nil {
			return nil, err
		}
		n.pending = pend
		return n, nil
	}
	// A path may name a directory of the path tree (a tagsDir, recursing) or
	// a meta leaf (.tags/.type of a materialized directory, or the tag file
	// of a file child). Directories win, matching the FUSE mount's lookup
	// order in the rare case a file and a directory share a name.
	own := "/" + strings.Join(segs, "/")
	uid, derr := s.tree.Tie.DirUIDFromPath(own)
	if derr == nil && uid != "" {
		return &node{
			kind:        kTagsDir,
			parentSlash: "/" + strings.Join(segs[:len(segs)-1], "/"),
			name:        segs[len(segs)-1],
			uid:         uid,
		}, nil
	}
	n, err := s.resolveMeta(canon, segs)
	if err == nil {
		return n, nil
	}
	return nil, err // report the meta-leaf miss: it names what the client asked about
}

// resolveMeta resolves one /tags leaf: ".tags"/".type" of the parent
// directory, or the tag file of a file child of the parent.
func (s *Share) resolveMeta(canon string, segs []string) (*node, error) {
	parentSlash := "/" + strings.Join(segs[:len(segs)-1], "/")
	name := segs[len(segs)-1]
	childSlash := path.Join(parentSlash, name)

	if name == tiefs.TagsSelfName || name == tiefs.TagsTypeName {
		uid, err := s.tree.Tie.DirUIDFromPath(parentSlash)
		if err != nil {
			return nil, err
		}
		if uid == "" {
			return nil, fmt.Errorf("smbfs: %q not found: %w", childSlash, fs.ErrNotExist)
		}
		return &node{kind: kMetaFile, parentSlash: parentSlash, name: name, uid: uid, hash: string(uid)}, nil
	}

	dir, err := s.tree.PathDir(parentSlash)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("smbfs: %q: %w", parentSlash, err)
		}
		return nil, fmt.Errorf("smbfs: %q not found: %w", childSlash, fs.ErrNotExist)
	}
	for _, f := range tiefs.DisambiguateFiles(dir.Files) {
		if f.Filename == name {
			return &node{kind: kMetaFile, parentSlash: parentSlash, name: name, hash: f.Uid}, nil
		}
	}
	return nil, fmt.Errorf("smbfs: %q not found: %w", childSlash, fs.ErrNotExist)
}

// binding returns the MetaBinding behind a kMetaFile node.
func (n *node) binding(t *tiefs.Tree) tiefs.MetaBinding {
	if n.name == tiefs.TagsTypeName {
		return t.DirTypesBinding(n.uid)
	}
	if n.uid != "" && (n.name == tiefs.TagsSelfName) {
		return t.TagsBinding(string(n.uid))
	}
	return t.TagsBinding(n.hash)
}

// metaInodeKey is the stable inode key for a meta file.
func (n *node) metaInodeKey() string {
	if n.name == tiefs.TagsTypeName {
		return tiefs.TypeInodeKey(n.uid)
	}
	if n.name == tiefs.TagsSelfName {
		return string(n.uid)
	}
	return n.hash
}

// Stat reports mode, size and inode for one path. Modes are full POSIX:
// directories 0o040755, writable files 0o100644, read-only files 0o100444
// (the smb server sets the READONLY attribute when mode&0o222 == 0). Stat has
// no timestamp field, so tie's TagDate cannot be surfaced over SMB.
func (s *Share) Stat(p string) (gofs.Stat, error) {
	n, err := s.resolve(p)
	if err != nil {
		return nil, err
	}
	switch n.kind {
	case kRoot:
		return gofs.NewStat(0o040755, 0, s.tree.InodeID("\x00root")), nil
	case kQueryRoot:
		return gofs.NewStat(0o040755, 0, s.tree.InodeID("\x00query")), nil
	case kQueryDir:
		return gofs.NewStat(0o040755, 0, s.tree.InodeID("\x00query\x00"+n.query)), nil
	case kStatic:
		return gofs.NewStat(0o100444, uint64(len(tiefs.ReadmeText)), s.tree.InodeID("\x00readme")), nil
	case kTagList:
		tags, err := s.tree.TagList()
		if err != nil {
			return nil, err
		}
		return gofs.NewStat(0o100444, uint64(len(tiefs.RenderMeta(tags))), s.tree.InodeID("\x00taglist")), nil
	case kTypeList:
		types, err := s.tree.TypeList()
		if err != nil {
			return nil, err
		}
		return gofs.NewStat(0o100444, uint64(len(tiefs.RenderMeta(types))), s.tree.InodeID("\x00typelist")), nil
	case kQueryFile:
		return gofs.NewStat(0o100444, uint64(n.file.Size), s.tree.InodeID(n.hash)), nil
	case kTaggedDir, kPathDir, kTagsDir:
		key := string(n.uid)
		if key == "" {
			key = "\x00" + viewFiles // unmaterialized root: stable placeholder
			if n.kind == kTagsDir {
				key = "\x00" + viewTags
			}
		}
		return gofs.NewStat(0o040755, 0, s.tree.InodeID(key)), nil
	case kArchiveDir:
		return gofs.NewStat(0o040755, 0, s.tree.InodeID(n.hash)), nil
	case kMember:
		return gofs.NewStat(0o100444, uint64(n.memberSize), s.tree.InodeID(tiefs.MemberKey(n.hash, n.member))), nil
	case kPathFile:
		if n.pending != nil {
			ino := s.tree.InodeID(n.parentSlash + "\x00" + n.name)
			return gofs.NewStat(0o100644, uint64(n.pending.size()), ino), nil
		}
		return gofs.NewStat(0o100644, uint64(n.file.Size), s.tree.InodeID(n.hash)), nil
	case kMetaFile:
		size := int64(0)
		if n.pending != nil {
			size = n.pending.size()
		} else {
			vals, err := n.binding(s.tree).Load()
			if err == nil {
				size = int64(len(tiefs.RenderMeta(vals)))
			}
		}
		return gofs.NewStat(0o100644, uint64(size), s.tree.InodeID(n.metaInodeKey())), nil
	}
	return nil, fmt.Errorf("smbfs: %q not found: %w", p, fs.ErrNotExist)
}
