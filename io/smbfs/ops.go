package smbfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"

	gofs "github.com/go-filesystems/interface"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"
)

// WriteFile implements the Filesystem write entry point.
//
// data == nil is the smb server's create-or-truncate call (its CREATE flow
// runs WriteFile(p,nil) before OpenFile, then Stats the path): the file is
// staged, not committed, and registered in pending so Stat/ListDir/OpenFile
// all see it. The commit happens when the handle closes.
//
// data != nil is the whole-file fallback (used by the server only when a
// handle is not a WritableFile): stage, write, commit, close.
//
// Read-only views answer fs.ErrPermission; a directory target fs.ErrExist.
func (s *Share) WriteFile(p string, data []byte, perm os.FileMode) error {
	segs := segments(p)
	if len(segs) < 2 {
		// The root and the three view roots are directories that always exist.
		if len(segs) == 1 && (segs[0] == viewQuery || segs[0] == viewFiles || segs[0] == viewTags) {
			return fmt.Errorf("smbfs: %q is a directory: %w", p, fs.ErrExist)
		}
		return errReadOnly
	}
	switch segs[0] {
	case viewQuery:
		return errReadOnly
	case viewFiles, viewTags:
	default:
		return fmt.Errorf("smbfs: %q not found: %w", p, fs.ErrNotExist)
	}

	// Reject targets that already exist as something unwritable.
	if n, err := s.resolve(p); err == nil {
		switch n.kind {
		case kQueryFile, kMember, kStatic, kTagList, kTypeList:
			return errReadOnly
		case kRoot, kQueryRoot, kQueryDir, kTaggedDir, kArchiveDir, kPathDir, kTagsDir:
			return fmt.Errorf("smbfs: %q is a directory: %w", p, fs.ErrExist)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	canon := canonPath(segs)
	parentSlash := "/" + strings.Join(segs[1:len(segs)-1], "/")
	name := segs[len(segs)-1]

	if segs[0] == viewTags {
		// A meta file must already have a binding: you cannot tag a file that
		// is not in the path tree, and .tags/.type exist only under
		// materialized directories. resolveMeta wants the segments below the
		// view name (its parentSlash is a tie path, not an fs path).
		n, err := s.resolveMeta(canon, segs[1:])
		if err != nil {
			return err
		}
		if data == nil {
			mh := &metaHandle{sh: s, path: canon, bind: n.binding(s.tree), forceCommit: true}
			s.registerPending(canon, &pendingEntry{
				parentSlash: n.parentSlash, name: n.name, meta: true,
				inoKey: n.metaInodeKey(), size: mh.size, h: mh,
			})
			return nil
		}
		return n.binding(s.tree).Store(tiesfs.ParseLines(data))
	}

	existing := s.lookupFile(parentSlash, name)
	if data == nil {
		wh, err := s.tree.NewWriteHandle(parentSlash, name, existing)
		if err != nil {
			return err
		}
		h := &dataHandle{
			sh: s, path: canon, parentSlash: parentSlash, name: name,
			file: existing, forceCommit: true, wh: wh,
		}
		s.registerPending(canon, &pendingEntry{
			parentSlash: parentSlash, name: name, meta: false,
			inoKey: parentSlash + "\x00" + name, size: h.size, h: h,
		})
		return nil
	}
	h := &dataHandle{
		sh: s, path: canon, parentSlash: parentSlash, name: name,
		file: existing, forceCommit: true,
	}
	if _, err := h.WriteAt(data, 0); err != nil {
		return err
	}
	return h.Close()
}

// lookupFile finds the stored file named name in parentSlash (disambiguated
// display names), or the zero client.File when there is none.
func (s *Share) lookupFile(parentSlash, name string) client.File {
	dir, err := s.tree.PathDir(parentSlash)
	if err != nil {
		return client.File{}
	}
	for _, f := range tiesfs.DisambiguateFiles(dir.Files) {
		if f.Filename == name {
			return f
		}
	}
	return client.File{}
}

// OpenFile returns a handle for a regular file. Pending (staged, uncommitted)
// paths return their staged handle, so the CREATE flow writes into the file
// it just created. Existing files open lazily: reads come from the blob
// cache, and the first write promotes to a staging handle.
func (s *Share) OpenFile(p string) (gofs.File, error) {
	segs := segments(p)
	canon := canonPath(segs)
	s.mu.Lock()
	pend := s.pending[canon]
	s.mu.Unlock()
	if pend != nil {
		return pend.h.(gofs.File), nil
	}
	n, err := s.resolve(p)
	if err != nil {
		return nil, err
	}
	switch n.kind {
	case kPathFile:
		return &dataHandle{
			sh: s, path: canon, parentSlash: n.parentSlash, name: n.name, file: n.file,
		}, nil
	case kQueryFile:
		return &lazyFile{sh: s, hash: n.hash, size: int64(n.file.Size)}, nil
	case kMember:
		return &lazyFile{sh: s, hash: n.hash, member: n.member, size: n.memberSize}, nil
	case kStatic:
		return &roFile{data: []byte(tiesfs.ReadmeText)}, nil
	case kTagList:
		tags, err := s.tree.TagList()
		if err != nil {
			return nil, err
		}
		return &roFile{data: tiesfs.RenderMeta(tags)}, nil
	case kTypeList:
		types, err := s.tree.TypeList()
		if err != nil {
			return nil, err
		}
		return &roFile{data: tiesfs.RenderMeta(types)}, nil
	case kMetaFile:
		vals, err := n.binding(s.tree).Load()
		if err != nil {
			return nil, err
		}
		return &metaHandle{sh: s, path: canon, bind: n.binding(s.tree), buf: tiesfs.RenderMeta(vals)}, nil
	}
	return nil, fmt.Errorf("smbfs: %q is not a regular file: %w", p, fs.ErrNotExist)
}

// lazyFile serves a read-only blob (query result or archive member),
// resolving the cached blob on first read so Open itself stays cheap.
type lazyFile struct {
	sh     *Share
	hash   string
	member string // "" for a plain blob
	size   int64

	mu sync.Mutex
	b  *tiesfs.Blob
}

var _ = (gofs.File)((*lazyFile)(nil))

func (f *lazyFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.b == nil {
		var b *tiesfs.Blob
		var err error
		if f.member == "" {
			b, err = f.sh.tree.Cache().Blob(f.hash)
		} else {
			b, err = f.sh.tree.Cache().Member(f.hash, f.member)
		}
		if err != nil {
			return 0, err
		}
		f.b = b
	}
	return f.b.ReadAt(p, off)
}

func (f *lazyFile) Size() int64 { return f.size }

func (f *lazyFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.b != nil {
		f.b.Close() // drop the reference taken on first read
		f.b = nil
	}
	return nil
}

// MkDir creates a subdirectory of the /files path tree. The /query and /tags
// views and the fixed top level are refused with fs.ErrPermission/fs.ErrExist.
func (s *Share) MkDir(p string, perm os.FileMode) error {
	segs := segments(p)
	if len(segs) < 2 {
		return fmt.Errorf("smbfs: %q exists: %w", p, fs.ErrExist)
	}
	if segs[0] != viewFiles {
		return errReadOnly
	}
	parentSlash := "/" + strings.Join(segs[1:len(segs)-1], "/")
	name := segs[len(segs)-1]
	_, err := s.tree.Mkdir(parentSlash, name)
	return err
}

// DeleteFile unlinks a file from the /files path tree.
func (s *Share) DeleteFile(p string) error {
	segs := segments(p)
	if len(segs) < 2 || segs[0] != viewFiles {
		return errReadOnly
	}
	canon := canonPath(segs)
	parentSlash := "/" + strings.Join(segs[1:len(segs)-1], "/")
	name := segs[len(segs)-1]
	if err := s.tree.DeleteFile(parentSlash, name); err != nil {
		return err
	}
	s.dropPendingByPath(canon)
	return nil
}

// DeleteDir removes an empty subdirectory of the /files path tree. A
// non-empty directory reports tiesfs.ErrNotEmpty, wrapped in fs.ErrExist:
// the smb server has no ENOTEMPTY status and maps ErrExist onto
// OBJECT_NAME_COLLISION, which is the closest honest answer.
func (s *Share) DeleteDir(p string) error {
	segs := segments(p)
	if len(segs) < 2 {
		return fmt.Errorf("smbfs: %q exists: %w", p, fs.ErrExist)
	}
	if segs[0] != viewFiles {
		return errReadOnly
	}
	parentSlash := "/" + strings.Join(segs[1:len(segs)-1], "/")
	name := segs[len(segs)-1]
	err := s.tree.DeleteDir(parentSlash, name)
	if errors.Is(err, tiesfs.ErrNotEmpty) {
		return fmt.Errorf("smbfs: %q not empty: %w", p, fs.ErrExist)
	}
	return err
}

// Rename moves a child within the /files path tree. The smb server ignores
// the client's ReplaceIfExists flag, so the move always replaces (the tiesfs
// layer still refuses to clobber a directory). A staged-but-uncommitted
// create (pending) is moved by re-registering it under the new path: the
// bytes commit to the new name when the handle closes.
func (s *Share) Rename(oldPath, newPath string) error {
	oldSegs, newSegs := segments(oldPath), segments(newPath)
	if len(oldSegs) < 2 || oldSegs[0] != viewFiles {
		return errReadOnly
	}
	if len(newSegs) < 2 || newSegs[0] != viewFiles {
		return errReadOnly
	}
	oldParent := "/" + strings.Join(oldSegs[1:len(oldSegs)-1], "/")
	oldName := oldSegs[len(oldSegs)-1]
	newParent := "/" + strings.Join(newSegs[1:len(newSegs)-1], "/")
	newName := newSegs[len(newSegs)-1]

	oldCanon, newCanon := canonPath(oldSegs), canonPath(newSegs)
	s.mu.Lock()
	pend := s.pending[oldCanon]
	if pend != nil {
		delete(s.pending, oldCanon)
		pend.parentSlash, pend.name, pend.inoKey = newParent, newName, newParent+"\x00"+newName
		s.pending[newCanon] = pend
	}
	s.mu.Unlock()
	if pend != nil {
		if dh, ok := pend.h.(*dataHandle); ok {
			dh.renameTo(newCanon, newParent, newName)
		}
		return nil
	}
	return s.tree.Rename(oldParent, oldName, newParent, newName, true)
}

// registerPending records a staged write. A second CREATE on the same path
// replaces the registration; the previous handle still commits through its
// own Close, matching last-writer-wins.
func (s *Share) registerPending(canon string, e *pendingEntry) {
	s.mu.Lock()
	s.pending[canon] = e
	s.mu.Unlock()
}

// dropPending removes the registration only if it still points at h.
func (s *Share) dropPending(canon string, h handle) {
	s.mu.Lock()
	if e, ok := s.pending[canon]; ok && e.h == h {
		delete(s.pending, canon)
	}
	s.mu.Unlock()
}

// dropPendingByPath removes any registration for a path (after a delete).
func (s *Share) dropPendingByPath(canon string) {
	s.mu.Lock()
	delete(s.pending, canon)
	s.mu.Unlock()
}
