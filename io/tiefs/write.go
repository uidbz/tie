package tiefs

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
		return 0, fmt.Errorf("tiefs: write handle closed")
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
		return fmt.Errorf("tiefs: write handle closed")
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
	newHash, err := t.Tie.WriteFile(t.host, parentUID, h.name, h.tmpPath, nil)
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
		if err != nil && h.dirty {
			// The staging file is the only copy of what the user wrote. Most
			// applications ignore close()/release() errors, so deleting it
			// here would throw away their data without a sound (§4.9): keep it
			// in the spool and put the path in the error.
			dest := h.parentPath + "/" + h.name
			if spool, serr := h.tree.spoolFailed(h.parentPath, h.name, h.tmpPath); serr == nil {
				// Logged as well as returned: a FUSE release() or an SMB close
				// error is usually ignored by the application, and this is the
				// only notice the user gets that their file is somewhere else.
				slog.Error("tiefs: write could not be committed; the bytes are kept in the spool",
					"file", dest, "spool", spool, "err", err)
				err = fmt.Errorf("%w (bytes kept in %s)", err, spool)
			} else {
				slog.Error("tiefs: write could not be committed and the bytes could not be kept",
					"file", dest, "err", err, "spool", serr)
				err = fmt.Errorf("%w (and the staged bytes could not be kept: %v)", err, serr)
			}
		} else {
			os.Remove(h.tmpPath)
		}
		h.tmp = nil
	}
	return err
}

// spoolFailed moves a staging file whose commit failed into the spool
// directory and returns where it landed. A rename is one syscall when the
// spool shares the staging filesystem; otherwise the bytes are copied, which
// is slow and still better than losing them.
func (s *Store) spoolFailed(parentSlash, name, tmpPath string) (string, error) {
	if err := os.MkdirAll(s.spoolDir, 0o700); err != nil {
		return "", err
	}
	base := sanitizeForDisk(name)
	// The name alone does not say where the file belonged, and two writes of
	// the same name must not collide.
	dst := filepath.Join(s.spoolDir, fmt.Sprintf("%s%s-%d", spoolPathPrefix(parentSlash), base, time.Now().UnixNano()))

	if err := os.Rename(tmpPath, dst); err == nil {
		return dst, nil
	}
	src, err := os.Open(tmpPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	os.Remove(tmpPath)
	return dst, nil
}

// spoolPathPrefix turns the intended directory into part of the spool file
// name: "/music/live" becomes "music_live_", so a spool of a whole failed
// album keeps its shape.
func spoolPathPrefix(parentSlash string) string {
	p := sanitizeForDisk(strings.Trim(strings.ReplaceAll(parentSlash, "/", "_"), "_"))
	if p == "" {
		return ""
	}
	return p + "_"
}

// sanitizeForDisk makes a tie name safe to use as a file name: tie names can
// contain '/' (a query path) or control bytes that a shell or a file manager
// would choke on.
func sanitizeForDisk(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == os.PathSeparator || r == 0:
			return '_'
		case r < 0x20 || r == 0x7f:
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		return "untitled"
	}
	const max = 100
	if len(name) > max {
		name = name[:max]
	}
	return name
}
