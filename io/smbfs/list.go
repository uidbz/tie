package smbfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	gofs "github.com/go-filesystems/interface"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"
)

// dirEntry types the interface module expects (memfs convention: 1 file,
// 2 dir). The smb server ignores FileType and Stats every child, but other
// consumers of ListDir do not.
const (
	typeFile = 1
	typeDir  = 2
)

// ListDir lists one directory. Display names are escaped (escape.go); the
// server Stats every name it is given, so every entry must resolve back.
// Pending (not yet committed) creates appear too, so a file a client has
// created but not closed yet is visible.
func (s *Share) ListDir(p string) ([]gofs.DirEntry, error) {
	n, err := s.resolve(p)
	if err != nil {
		return nil, err
	}
	segs := segments(p)

	switch n.kind {
	case kRoot:
		return []gofs.DirEntry{
			gofs.NewDirEntry(s.tree.InodeID("\x00"+viewQuery), viewQuery, typeDir),
			gofs.NewDirEntry(s.tree.InodeID("\x00"+viewFiles), viewFiles, typeDir),
			gofs.NewDirEntry(s.tree.InodeID("\x00"+viewTags), viewTags, typeDir),
		}, nil

	case kQueryRoot:
		entries := []gofs.DirEntry{
			gofs.NewDirEntry(s.tree.InodeID("\x00readme"), tiesfs.ReadmeName, typeFile),
			gofs.NewDirEntry(s.tree.InodeID("\x00taglist"), tiesfs.TagListName, typeFile),
			gofs.NewDirEntry(s.tree.InodeID("\x00typelist"), tiesfs.TypeListName, typeFile),
		}
		for name, expr := range s.tree.Tie.Config.Queries {
			// Key the inode by the query expression, the same key Stat uses
			// once the client resolves this entry, so listing and opening
			// agree on the file id.
			entries = append(entries, gofs.NewDirEntry(s.tree.InodeID("\x00query\x00"+expr), escapeName(name), typeDir))
		}
		return entries, nil

	case kQueryDir:
		scope, include, exclude := tiesfs.ParseTagQuery(n.query)
		files, err := s.tree.QueryFiles(scope, include, exclude)
		if err != nil {
			return nil, err
		}
		entries := make([]gofs.DirEntry, 0, len(files))
		for _, f := range files {
			t := uint8(typeFile)
			if f.IsDir || f.IsArchive {
				t = typeDir
			}
			entries = append(entries, gofs.NewDirEntry(s.tree.InodeID(f.Hash), escapeName(f.Filename), t))
		}
		return entries, nil

	case kTaggedDir:
		return s.listTieDir(n.uid, false, "")

	case kPathDir:
		return s.listTieDir(n.uid, true, ownSlash(segs, 1))

	case kTagsDir:
		return s.listTagsDir(n, ownSlash(segs, 1))

	case kArchiveDir:
		members, err := s.tree.Cache().ArchiveMembers(n.hash)
		if err != nil {
			return nil, err
		}
		views := tiesfs.ArchiveMemberViews(members)
		entries := make([]gofs.DirEntry, 0, len(views))
		for _, v := range views {
			entries = append(entries, gofs.NewDirEntry(
				s.tree.InodeID(tiesfs.MemberKey(n.hash, v.Member().Name)),
				escapeName(v.Display()), typeFile))
		}
		return entries, nil
	}
	return nil, fmt.Errorf("smbfs: %q is not a directory: %w", p, fs.ErrNotExist)
}

// ownSlash rebuilds the tie slash path a /files or /tags node addresses from
// the fs path segments, starting after the view name.
func ownSlash(segs []string, from int) string {
	if from >= len(segs) {
		return "/"
	}
	return "/" + strings.Join(segs[from:], "/")
}

// listTieDir lists one live path-tree directory (ReadTieDir children).
// An unmaterialized directory (no triples yet - e.g. the /files root on a
// fresh store) lists empty rather than failing, so it can be populated.
// withPending adds staged-but-uncommitted creates for this directory.
func (s *Share) listTieDir(uid client.DirUID, withPending bool, own string) ([]gofs.DirEntry, error) {
	var dir client.Directory
	if uid != "" {
		var err error
		dir, err = client.ReadTieDir(s.tree.Tie, uid)
		if err != nil {
			return nil, err
		}
	}
	entries := make([]gofs.DirEntry, 0, len(dir.SubDirs)+len(dir.Files)+len(dir.Archives))
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := tiesfs.BaseName(p); name != "" {
				entries = append(entries, gofs.NewDirEntry(s.tree.InodeID(string(sub.Uid)), escapeName(name), typeDir))
			}
		}
	}
	for _, a := range dir.Archives {
		entries = append(entries, gofs.NewDirEntry(s.tree.InodeID(a.Hash), escapeName(a.Filename), typeDir))
	}
	for _, f := range tiesfs.DisambiguateFiles(dir.Files) {
		entries = append(entries, gofs.NewDirEntry(s.tree.InodeID(f.Uid), escapeName(f.Filename), typeFile))
	}
	if withPending {
		entries = append(entries, s.pendingEntries(own, false)...)
	}
	return entries, nil
}

// listTagsDir lists one /tags directory: the .tags/.type control files (when
// the directory is materialized), its subdirectories, its files as writable
// tag leaves, and pending meta writes.
func (s *Share) listTagsDir(n *node, own string) ([]gofs.DirEntry, error) {
	var dir client.Directory
	if n.uid != "" {
		var err error
		dir, err = client.ReadTieDir(s.tree.Tie, n.uid)
		if err != nil {
			return nil, err
		}
	}
	entries := make([]gofs.DirEntry, 0, len(dir.SubDirs)+len(dir.Files)+2)
	if n.uid != "" {
		entries = append(entries,
			gofs.NewDirEntry(s.tree.InodeID(string(n.uid)), tiesfs.TagsSelfName, typeFile),
			gofs.NewDirEntry(s.tree.InodeID(tiesfs.TypeInodeKey(n.uid)), tiesfs.TagsTypeName, typeFile),
		)
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if name := tiesfs.BaseName(p); name != "" {
				entries = append(entries, gofs.NewDirEntry(s.tree.InodeID(string(sub.Uid)), escapeName(name), typeDir))
			}
		}
	}
	for _, f := range tiesfs.DisambiguateFiles(dir.Files) {
		entries = append(entries, gofs.NewDirEntry(s.tree.InodeID(f.Uid), escapeName(f.Filename), typeFile))
	}
	entries = append(entries, s.pendingEntries(own, true)...)
	return entries, nil
}

// pendingEntries lists staged-but-uncommitted writes whose parent directory
// has the tie slash path own.
func (s *Share) pendingEntries(own string, meta bool) []gofs.DirEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []gofs.DirEntry
	for _, e := range s.pending {
		if e.meta != meta || e.parentSlash != own {
			continue
		}
		out = append(out, gofs.NewDirEntry(s.tree.InodeID(e.inoKey), escapeName(e.name), typeFile))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// ReadFile returns the whole file. The smb server only uses it as a fallback
// (it prefers Opener); it is kept correct and simple.
func (s *Share) ReadFile(p string) ([]byte, error) {
	n, err := s.resolve(p)
	if err != nil {
		return nil, err
	}
	switch n.kind {
	case kStatic:
		return []byte(tiesfs.ReadmeText), nil
	case kTagList:
		tags, err := s.tree.TagList()
		if err != nil {
			return nil, err
		}
		return tiesfs.RenderMeta(tags), nil
	case kTypeList:
		types, err := s.tree.TypeList()
		if err != nil {
			return nil, err
		}
		return tiesfs.RenderMeta(types), nil
	case kQueryFile, kPathFile:
		if n.pending != nil {
			if dh, ok := n.pending.h.(*dataHandle); ok {
				return dh.readAll()
			}
			if mh, ok := n.pending.h.(*metaHandle); ok {
				return mh.readAll(), nil
			}
		}
		b, err := s.tree.Cache().Blob(n.hash)
		if err != nil {
			return nil, err
		}
		return readAll(b)
	case kMember:
		b, err := s.tree.Cache().Member(n.hash, n.member)
		if err != nil {
			return nil, err
		}
		return readAll(b)
	case kMetaFile:
		if n.pending != nil {
			if mh, ok := n.pending.h.(*metaHandle); ok {
				return mh.readAll(), nil
			}
		}
		vals, err := n.binding(s.tree).Load()
		if err != nil {
			return nil, err
		}
		return tiesfs.RenderMeta(vals), nil
	}
	return nil, fmt.Errorf("smbfs: %q is not a regular file: %w", p, fs.ErrNotExist)
}

// ReadLink: the tree has no symlinks, and the smb server never calls this.
func (s *Share) ReadLink(p string) (string, error) {
	return "", fmt.Errorf("smbfs: %q is not a symlink: %w", p, fs.ErrNotExist)
}

// readAll drains a Blob following io.ReaderAt semantics.
func readAll(b *tiesfs.Blob) ([]byte, error) {
	out := make([]byte, b.Size())
	if _, err := b.ReadAt(out, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return out, nil
}
