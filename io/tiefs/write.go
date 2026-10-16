package tiesfs

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/uidbz/tie/client"
)

// WriteHandle stages a file's new bytes to a temp file and commits them to the
// store on Commit/Close. Backing the buffer with a file (not memory) keeps
// large writes memory-bounded, like the read cache. The commit uploads the
// bytes and (re)writes the file's triples via client.WriteFile, which versions
// any superseded content into <name>_prev and no-ops when the bytes are
// unchanged (identical hash). Commit is idempotent.
//
// existing is the currently stored file (zero Uid for a not-yet-committed
// Create); Seed copies its bytes into the staging file so a partial in-place
// edit preserves the untouched bytes.
type WriteHandle struct {
	tree       *Tree
	parentPath string // slash path of the containing directory (e.g. "/music")
	name       string // this file's name within parentPath
	file       client.File

	tmp     *os.File
	tmpPath string

	mu        sync.Mutex
	dirty     bool
	committed bool
	closed    bool
}

// NewWriteHandle stages a write of name inside parentSlash. existing is the
// current stored file, or the zero client.File for a fresh Create.
func (t *Tree) NewWriteHandle(parentSlash, name string, existing client.File) (*WriteHandle, error) {
	tmp, err := os.CreateTemp(t.writeDir, "w-*")
	if err != nil {
		return nil, err
	}
	return &WriteHandle{tree: t, parentPath: parentSlash, name: name, file: existing, tmp: tmp, tmpPath: tmp.Name()}, nil
}

// Seed copies the current stored content into the staging file so a partial
// in-place edit (write at an offset without a preceding truncate) preserves the
// untouched bytes. Bounded memory: io.Copy streams through its buffer.
func (h *WriteHandle) Seed() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tmp == nil || h.file.Uid == "" {
		return nil
	}
	e, err := h.tree.cache.Blob(h.file.Uid)
	if err != nil {
		return err
	}
	defer e.Close()
	_, err = io.Copy(h.tmp, io.NewSectionReader(e, 0, e.Size()))
	return err
}

// WriteAt writes len(p) bytes at off, marking the handle dirty.
func (h *WriteHandle) WriteAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tmp == nil {
		return 0, fmt.Errorf("tiesfs: write handle closed")
	}
	n, err := h.tmp.WriteAt(p, off)
	if err != nil {
		return n, err
	}
	h.dirty = true
	return n, nil
}

// ReadAt reads from the staged bytes (not yet committed), following
// io.ReaderAt semantics.
func (h *WriteHandle) ReadAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tmp == nil {
		return 0, io.EOF
	}
	return h.tmp.ReadAt(p, off)
}

// Truncate resizes the staged bytes.
func (h *WriteHandle) Truncate(size int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tmp == nil {
		return fmt.Errorf("tiesfs: write handle closed")
	}
	if err := h.tmp.Truncate(size); err != nil {
		return err
	}
	h.dirty = true
	return nil
}

// Size reports the staged length.
func (h *WriteHandle) Size() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tmp == nil {
		return int64(h.file.Size)
	}
	fi, err := h.tmp.Stat()
	if err != nil {
		return int64(h.file.Size)
	}
	return fi.Size()
}

// Dirty reports whether any write or truncate has landed.
func (h *WriteHandle) Dirty() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dirty
}

// Rename retargets an uncommitted handle: the staged bytes will commit as
// newName inside parentSlash instead of the original location. Only legal
// before the first Commit (a transport renaming a file it created but has
// not closed yet).
func (h *WriteHandle) Rename(parentSlash, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.committed {
		return
	}
	h.parentPath, h.name = parentSlash, name
}

// File reports the stored file as currently known (updated after Commit).
func (h *WriteHandle) File() client.File {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.file
}

// Commit uploads the staged bytes and (re)writes the file's triples via
// client.WriteFile. It is idempotent: a second Commit is a no-op. The caller
// must not write concurrently.
func (h *WriteHandle) Commit() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.commitLocked()
}

func (h *WriteHandle) commitLocked() error {
	if h.committed || h.tmp == nil {
		return nil
	}
	if err := h.tmp.Sync(); err != nil {
		return err
	}
	t := h.tree
	parentUID, err := t.Tie.DirUIDFromPath(h.parentPath)
	if err != nil {
		return err
	}
	if parentUID == "" {
		if parentUID, err = t.Tie.MkTieDirAll(client.FileURIScheme + h.parentPath); err != nil {
			return err
		}
	}
	newHash, err := t.Tie.WriteFile(t.host, "", parentUID, h.name, h.tmpPath, nil)
	if err != nil {
		return err
	}
	h.committed = true
	h.file.Uid = newHash
	if fi, err := h.tmp.Stat(); err == nil {
		h.file.Size = int(fi.Size())
	}
	return nil
}

// Close commits (if not already committed) and drops the staging temp file.
// It is safe to call more than once.
func (h *WriteHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	var err error
	if h.tmp != nil {
		err = h.commitLocked()
		h.tmp.Close()
		os.Remove(h.tmpPath)
		h.tmp = nil
	}
	return err
}
