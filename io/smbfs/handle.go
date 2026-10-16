package smbfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"

	gofs "github.com/go-filesystems/interface"
	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/tiefs"
)

// handle is the shared pending-entry surface: size for Stat/ListDir, and
// close for cleanup.
type handle interface {
	size() int64
	closeHandle() error
}

// dataHandle is a writable file in the /files path tree (or a query-result
// file opened read-only). It implements gofs.File and gofs.WritableFile.
//
// Two modes:
//
//   - Staged (wh != nil): every read and write goes to the tiesfs staging
//     temp file; Close commits the bytes to the store. WriteFile(p, nil)
//     creates one immediately (the smb CREATE flow: stage, Stat, OpenFile).
//   - Lazy (wh == nil): reads are served straight from the blob cache; the
//     first WriteAt/Truncate stages a handle and Seeds it from the stored
//     content, so a partial in-place edit preserves untouched bytes.
//
// Sync is deliberately NOT a store commit: the smb server calls Sync after
// every WRITE request (up to one per MiB), and committing per request would
// re-upload the whole staged file each time and version every intermediate
// state into <name>_prev. Sync only flushes the staging file; the commit
// happens on Close, which the server always performs - including when a
// client connection drops (it closes every open handle).
type dataHandle struct {
	sh          *Share
	path        string // canonical fs path (pending-map key)
	parentSlash string // tie slash path of the containing directory
	name        string // unescaped file name
	file        client.File
	forceCommit bool // created by WriteFile(p,nil): commit even if never written

	mu     sync.Mutex
	wh     *tiesfs.WriteHandle // non-nil once staged
	blob   *tiesfs.Blob        // lazy read source, resolved on first read
	closed bool
}

var (
	_ = (gofs.File)((*dataHandle)(nil))
	_ = (gofs.WritableFile)((*dataHandle)(nil))
)

// stageLocked promotes a lazy handle to staged mode, seeding the staging file
// from the stored content so partial edits keep the untouched bytes.
func (h *dataHandle) stageLocked() error {
	if h.wh != nil {
		return nil
	}
	wh, err := h.sh.tree.NewWriteHandle(h.parentSlash, h.name, h.file)
	if err != nil {
		return err
	}
	if h.file.Uid != "" {
		if err := wh.Seed(); err != nil {
			wh.Close()
			return err
		}
	}
	h.wh = wh
	h.blob = nil
	return nil
}

func (h *dataHandle) WriteAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, fmt.Errorf("smbfs: handle closed")
	}
	if err := h.stageLocked(); err != nil {
		return 0, err
	}
	return h.wh.WriteAt(p, off)
}

func (h *dataHandle) Truncate(size int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return fmt.Errorf("smbfs: handle closed")
	}
	if size < 0 {
		return fmt.Errorf("smbfs: negative truncate %d", size)
	}
	if err := h.stageLocked(); err != nil {
		return err
	}
	return h.wh.Truncate(size)
}

func (h *dataHandle) ReadAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.wh != nil {
		return h.wh.ReadAt(p, off)
	}
	if h.blob == nil {
		if h.file.Uid == "" {
			return 0, io.EOF // created but nothing staged yet
		}
		b, err := h.sh.tree.Cache().Blob(h.file.Uid)
		if err != nil {
			return 0, err
		}
		h.blob = b
	}
	return h.blob.ReadAt(p, off)
}

func (h *dataHandle) Size() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.wh != nil {
		return h.wh.Size()
	}
	return int64(h.file.Size)
}

// Sync flushes the staging file but does not commit; see the type comment.
func (h *dataHandle) Sync() error { return nil }

// Close commits (staged handles always commit; forceCommit covers the
// create-then-close-without-writing case, matching O_TRUNC/O_CREAT
// semantics) and drops the pending registration.
func (h *dataHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	var err error
	if h.wh != nil {
		err = h.wh.Close()
		if err == nil {
			h.file = h.wh.File()
		}
	}
	if h.blob != nil {
		// Release the cache reference this handle took on first read.
		h.blob.Close()
		h.blob = nil
	}
	h.sh.dropPending(h.path, h)
	return err
}

func (h *dataHandle) size() int64        { return h.Size() }
func (h *dataHandle) closeHandle() error { return h.Close() }

// renameTo retargets a staged handle after a pending rename, so the commit
// lands under the new name.
func (h *dataHandle) renameTo(canon, parentSlash, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.path, h.parentSlash, h.name = canon, parentSlash, name
	if h.wh != nil {
		h.wh.Rename(parentSlash, name)
	}
}

// readAll returns the staged bytes (pending path reads via ReadFile).
func (h *dataHandle) readAll() ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	size := int64(0)
	if h.wh != nil {
		size = h.wh.Size()
	}
	out := make([]byte, size)
	if size == 0 {
		return out, nil
	}
	if _, err := h.wh.ReadAt(out, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return out, nil
}

// metaHandle is a writable tag/type text file under /tags. Its buffer is the
// complete future content: reading snapshots the current values, writing
// replaces them, and commit parses the buffer into a set (one value per
// line) and stores it. Unlike dataHandle, Sync DOES commit: meta files are
// tiny, and replacing a set twice in a row is harmless (last write wins).
type metaHandle struct {
	sh          *Share
	path        string
	bind        tiesfs.MetaBinding
	forceCommit bool // created by WriteFile(p,nil): commit even if never written

	mu     sync.Mutex
	buf    []byte
	dirty  bool
	closed bool
}

var (
	_ = (gofs.File)((*metaHandle)(nil))
	_ = (gofs.WritableFile)((*metaHandle)(nil))
)

func (h *metaHandle) WriteAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, fmt.Errorf("smbfs: handle closed")
	}
	if off < 0 {
		return 0, fmt.Errorf("smbfs: negative write offset %d", off)
	}
	end := off + int64(len(p))
	if int64(len(h.buf)) < end {
		grown := make([]byte, end)
		copy(grown, h.buf)
		h.buf = grown
	}
	copy(h.buf[off:end], p)
	h.dirty = true
	return len(p), nil
}

func (h *metaHandle) Truncate(size int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return fmt.Errorf("smbfs: handle closed")
	}
	if size < 0 {
		return fmt.Errorf("smbfs: negative truncate %d", size)
	}
	if int64(len(h.buf)) != size {
		grown := make([]byte, size)
		copy(grown, h.buf)
		h.buf = grown
		h.dirty = true
	}
	return nil
}

func (h *metaHandle) ReadAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if off < 0 || off >= int64(len(h.buf)) {
		if off < 0 {
			return 0, fmt.Errorf("smbfs: negative read offset %d", off)
		}
		return 0, io.EOF
	}
	n := copy(p, h.buf[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (h *metaHandle) Size() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return int64(len(h.buf))
}

// Sync commits the buffer when it is (or must become) the backing set.
func (h *metaHandle) Sync() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.dirty && !h.forceCommit {
		return nil
	}
	return h.bind.Store(tiesfs.ParseLines(h.buf))
}

func (h *metaHandle) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	err := h.Sync()
	h.sh.dropPending(h.path, h)
	return err
}

func (h *metaHandle) size() int64        { return h.Size() }
func (h *metaHandle) closeHandle() error { return h.Close() }

func (h *metaHandle) readAll() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]byte, len(h.buf))
	copy(out, h.buf)
	return out
}

// roFile is a read-only gofs.File over fixed bytes or a cached blob. It
// deliberately does NOT implement WritableFile: the smb server then refuses
// positional writes on it, and its whole-file write fallback lands in
// WriteFile, which answers fs.ErrPermission for read-only paths.
type roFile struct {
	data []byte
	blob *tiesfs.Blob
}

var _ = (gofs.File)((*roFile)(nil))

func (f *roFile) ReadAt(p []byte, off int64) (int, error) {
	if f.blob != nil {
		return f.blob.ReadAt(p, off)
	}
	if off < 0 || off >= int64(len(f.data)) {
		if off < 0 {
			return 0, fmt.Errorf("smbfs: negative read offset %d", off)
		}
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *roFile) Size() int64 {
	if f.blob != nil {
		return f.blob.Size()
	}
	return int64(len(f.data))
}

func (f *roFile) Close() error { return nil }

// errReadOnly is returned for any mutation attempt on the read-only views
// (/query results, archive members, README, tag/type listings).
var errReadOnly = fmt.Errorf("smbfs: read-only view: %w", fs.ErrPermission)
