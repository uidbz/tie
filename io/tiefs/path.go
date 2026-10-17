package tiefs

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/uidbz/tie/client"
)

// ErrRenameLoop refuses moving a directory into its own subtree.
var ErrRenameLoop = errors.New("cannot move a directory into itself")

// ErrNotEmpty marks a delete of a directory that still has children.
var ErrNotEmpty = errors.New("directory not empty")

// PathDir resolves a slash path in the path tree (the tie:/... hierarchy
// built by import/MkTieDir) and reads that directory's current children. A
// path with no triples yet reports an error wrapping fs.ErrNotExist.
func (t *Tree) PathDir(slashPath string) (client.Directory, error) {
	uid, err := t.Tie.DirUIDFromPath(slashPath)
	if err != nil {
		return client.Directory{}, err
	}
	if uid == "" {
		return client.Directory{}, fmt.Errorf("tiefs: path %q not found: %w", slashPath, os.ErrNotExist)
	}
	return client.ReadTieDir(t.Tie, uid)
}

// Mkdir creates a subdirectory of parentSlash named name in the path tree,
// writing the directory's triples (parent/path/tie-type) to the store via
// MkTieDirAll. MkTieDirAll (not MkTieDir) is used so the "/" root - and any
// not-yet-materialized ancestor - is created too; on a fresh store the root
// does not exist yet, and MkTieDir would parent the new dir under an empty
// root UID, orphaning it from the /files view. An existing child reports an
// error wrapping fs.ErrExist.
func (t *Tree) Mkdir(parentSlash, name string) (client.DirUID, error) {
	childSlash := ChildPath(parentSlash, name)
	if existing, err := t.Tie.DirUIDFromPath(childSlash); err == nil && existing != "" {
		return "", fmt.Errorf("tiefs: %q exists: %w", childSlash, os.ErrExist)
	}
	uid, err := t.Tie.MkTieDirAll(client.FileURIScheme + childSlash)
	if err != nil {
		return "", err
	}
	if err := t.Tie.Sync(); err != nil {
		return "", err
	}
	return uid, nil
}

// Rename moves a child of parentSlash (name) to under dstSlash as newName,
// writing the change back to the triple store. When replace is false, a
// collision with an existing destination file reports an error wrapping
// fs.ErrExist (RENAME_NOREPLACE / SMB ReplaceIfExists=false); a collision with
// an existing destination directory is always refused, matching the mount's
// long-standing behavior. A missing source or parent wraps fs.ErrNotExist.
func (t *Tree) Rename(parentSlash, name, dstSlash, newName string, replace bool) error {
	srcParent, err := t.Tie.DirUIDFromPath(parentSlash)
	if err != nil || srcParent == "" {
		return fmt.Errorf("tiefs: source parent %q not found: %w", parentSlash, os.ErrNotExist)
	}
	dstParent, err := t.Tie.DirUIDFromPath(dstSlash)
	if err != nil || dstParent == "" {
		return fmt.Errorf("tiefs: destination parent %q not found: %w", dstSlash, os.ErrNotExist)
	}

	dir, err := t.PathDir(parentSlash)
	if err != nil {
		return err
	}

	// Locate the source entry: a file (subject = content hash) or a subdir.
	for _, f := range dir.Files {
		if f.Filename != name {
			continue
		}
		if !replace && destExists(t, dstSlash, newName) {
			return fmt.Errorf("tiefs: destination %q exists: %w", ChildPath(dstSlash, newName), os.ErrExist)
		}
		return client.RenameFile(t.Tie, f.Uid, srcParent, dstParent, newName)
	}
	for _, sub := range dir.SubDirs {
		match := false
		for _, p := range sub.Paths {
			if BaseName(p) == name {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		oldPath := client.FileURIScheme + ChildPath(parentSlash, name)
		newPath := client.FileURIScheme + ChildPath(dstSlash, newName)
		// Moving a directory inside itself is what POSIX rename answers EINVAL
		// for: the subtree stops being reachable from the root, which is the
		// cycle verify reports. Refuse it here rather than let a mount or an
		// SMB client write that state into the collection.
		if newPath == oldPath {
			return nil // a rename onto itself is a no-op, as POSIX defines it
		}
		if strings.HasPrefix(newPath, oldPath+"/") {
			return fmt.Errorf("tiefs: cannot move %q into itself (to %q): %w",
				ChildPath(parentSlash, name), ChildPath(dstSlash, newName), ErrRenameLoop)
		}
		// Reject collisions with an existing destination directory.
		if existing, err := t.Tie.DirUIDFromPath(ChildPath(dstSlash, newName)); err == nil && existing != "" {
			return fmt.Errorf("tiefs: destination %q exists: %w", ChildPath(dstSlash, newName), os.ErrExist)
		}
		return client.RenameDir(t.Tie, sub.Uid, oldPath, newPath, srcParent, dstParent)
	}
	return fmt.Errorf("tiefs: %q not found: %w", ChildPath(parentSlash, name), os.ErrNotExist)
}

// DeleteFile unlinks the child of parentSlash shown as displayName (the
// disambiguated name a listing would have produced) from the path tree. The
// blob stays in the filehost; retention governs its bytes.
func (t *Tree) DeleteFile(parentSlash, displayName string) error {
	dir, err := t.PathDir(parentSlash)
	if err != nil {
		return err
	}
	for _, f := range DisambiguateFiles(dir.Files) {
		if f.Filename == displayName {
			return client.DeleteFile(t.Tie, f.Uid, dir.Uid)
		}
	}
	return fmt.Errorf("tiefs: %q not found: %w", ChildPath(parentSlash, displayName), os.ErrNotExist)
}

// DeleteDir removes the empty subdirectory of parentSlash named name.
func (t *Tree) DeleteDir(parentSlash, name string) error {
	dir, err := t.PathDir(parentSlash)
	if err != nil {
		return err
	}
	for _, sub := range dir.SubDirs {
		match := false
		for _, p := range sub.Paths {
			if BaseName(p) == name {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		err := client.DeleteDir(t.Tie, sub.Uid, dir.Uid)
		if err != nil && strings.Contains(err.Error(), "not empty") {
			return fmt.Errorf("tiefs: %q not empty: %w", ChildPath(parentSlash, name), ErrNotEmpty)
		}
		return err
	}
	return fmt.Errorf("tiefs: %q not found: %w", ChildPath(parentSlash, name), os.ErrNotExist)
}

// destExists reports whether dstSlash already has a child (file or subdir)
// named newName.
func destExists(t *Tree, dstSlash, newName string) bool {
	dir, err := t.PathDir(dstSlash)
	if err != nil {
		return false
	}
	for _, f := range dir.Files {
		if f.Filename == newName {
			return true
		}
	}
	for _, sub := range dir.SubDirs {
		for _, p := range sub.Paths {
			if BaseName(p) == newName {
				return true
			}
		}
	}
	return false
}
