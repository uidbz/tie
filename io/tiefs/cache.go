package tiesfs

import (
	"encoding/hex"
	"errors"
	"fmt"
	stdhash "hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/metadata"
)

// Cache is a disk-backed, single-flight blob cache. Blobs are streamed from the
// filehost to files under dir (bounded by disk, not RAM), verified against their
// content hash while streaming, and served to reads via ReadAt. Concurrent reads
// of the same hash coalesce into a single download.
//
// It is the shared read path for every tie transport (FUSE mount, SMB server):
// a blob is downloaded once no matter how many protocol handles ask for it.
type Cache struct {
	filehost string
	client   *http.Client
	dir      string
	maxSize  int64
	verify   bool // hash downloaded bytes and check against their content address

	// mu guards entries and the LRU order slice. Downloads happen outside the
	// lock; the per-blob entry carries its own readiness signal.
	mu      sync.Mutex
	entries map[string]*cacheEntry
	order   []string // hashes in insertion order, for size-based eviction
	curSize int64
}

// cacheEntry is one cached blob. ready is closed once the download finishes (or
// fails); until then, readers block on it. On success, file is an open,
// read-only handle to the on-disk blob and size is its length; on failure, err
// is set. Keeping the fd open means an evicted-and-unlinked blob stays readable
// for handles that already resolved it (unlink removes the name, not the inode).
type cacheEntry struct {
	ready chan struct{}
	file  *os.File
	size  int64
	err   error
}

// NewCache builds a blob cache reading from filehostURL with the given HTTP
// client. maxBytes is the LRU eviction budget; verify controls whether
// downloaded bytes are hashed and checked against their content address (off
// for a trusted filehost, on when the filehost is untrusted).
func NewCache(filehostURL string, client *http.Client, maxBytes int64, verify bool) *Cache {
	dir, err := os.MkdirTemp("", "tie-cache-")
	if err != nil {
		// Fall back to the OS temp root; download() will surface open errors.
		dir = os.TempDir()
	}
	return &Cache{
		filehost: filehostURL,
		client:   client,
		dir:      dir,
		maxSize:  maxBytes,
		verify:   verify,
		entries:  make(map[string]*cacheEntry),
	}
}

// Close removes the on-disk cache directory and closes open blob handles. Safe
// to call once at shutdown.
func (c *Cache) Close() error {
	c.mu.Lock()
	for _, e := range c.entries {
		if e.file != nil {
			e.file.Close()
		}
	}
	c.entries = make(map[string]*cacheEntry)
	c.order = nil
	c.curSize = 0
	dir := c.dir
	c.mu.Unlock()
	if dir == "" || dir == os.TempDir() {
		return nil
	}
	return os.RemoveAll(dir)
}

// blobPath returns the on-disk path for a hash. The hash is validated as hex by
// callers, so it is safe as a single path segment.
func (c *Cache) blobPath(hash string) string {
	return filepath.Join(c.dir, hash)
}

// evictLocked drops least-recently-inserted blobs until curSize fits maxSize.
// The open fd of an evicted entry is closed and its file unlinked; any reader
// still holding a reference to that entry keeps reading through its own fd.
// Must be called with c.mu held.
func (c *Cache) evictLocked(keep string) {
	for c.curSize > c.maxSize && len(c.order) > 0 {
		oldest := c.order[0]
		if oldest == keep {
			// Never evict the blob we just admitted; stop to avoid churn.
			if len(c.order) == 1 {
				break
			}
			c.order = append(c.order[1:], oldest)
			continue
		}
		c.order = c.order[1:]
		e, ok := c.entries[oldest]
		if !ok {
			continue
		}
		delete(c.entries, oldest)
		c.curSize -= e.size
		if e.file != nil {
			e.file.Close()
		}
		os.Remove(c.blobPath(oldest))
	}
}

// Blob returns a random-access handle to the blob for hash, downloading it on
// the first request and coalescing concurrent requests for the same hash into
// that single download.
func (c *Cache) Blob(hash string) (*Blob, error) {
	if !metadata.IsHexHash(hash) {
		return nil, fmt.Errorf("tiesfs: invalid content hash %q", hash)
	}
	e, err := c.get(hash, func() (*os.File, int64, error) { return c.download(hash) })
	if err != nil {
		return nil, err
	}
	return &Blob{entry: e}, nil
}

// get returns the ready cache entry for key, running producer on the first
// request and coalescing concurrent requests for the same key into that single
// run. producer must return an open, read-only file (already unlinked) plus its
// size. Used both for whole-blob downloads (key = content hash) and extracted
// archive members (key = archive hash + member name).
func (c *Cache) get(key string, producer func() (*os.File, int64, error)) (*cacheEntry, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.mu.Unlock()
		<-e.ready
		return e, e.err
	}
	e := &cacheEntry{ready: make(chan struct{})}
	c.entries[key] = e
	c.mu.Unlock()

	// This goroutine won the race to create the entry, so it owns the work.
	e.file, e.size, e.err = producer()
	if e.err != nil {
		// Drop the failed entry so a later read can retry rather than caching
		// the error forever.
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		close(e.ready)
		return nil, e.err
	}

	c.mu.Lock()
	c.order = append(c.order, key)
	c.curSize += e.size
	c.evictLocked(key)
	c.mu.Unlock()

	close(e.ready)
	return e, nil
}

// download streams the blob from the filehost to a file in the cache dir and
// returns an open, read-only handle to it plus its size. Memory use is bounded
// by the copy buffer, not the file size, so blobs larger than RAM (or the cache
// budget) download and serve fine. When c.verify is set, the bytes are hashed as
// they copy and checked against their content address (the filehost is treated
// as untrusted); by default verification is skipped, since hashing every read of
// multi-GB media on a trusted personal filehost is wasted work.
func (c *Cache) download(hash string) (*os.File, int64, error) {
	resp, err := c.client.Get(c.filehost + "/" + hash)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("tiesfs: bad status %s", resp.Status)
	}

	path := c.blobPath(hash)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		return nil, 0, err
	}

	var dst io.Writer = f
	var h stdhash.Hash
	if c.verify {
		h, err = metadata.NewHash()
		if err != nil {
			f.Close()
			os.Remove(path)
			return nil, 0, err
		}
		dst = io.MultiWriter(f, h)
	}
	size, err := io.Copy(dst, resp.Body)
	if err != nil {
		f.Close()
		os.Remove(path)
		return nil, 0, err
	}
	if c.verify {
		if got := hex.EncodeToString(h.Sum(nil)); got != hash {
			f.Close()
			os.Remove(path)
			return nil, 0, errors.New("tiesfs: downloaded content does not match its hash")
		}
	}

	// Unlink now: the open fd keeps the bytes reachable, and eviction/close no
	// longer needs a second unlink. The dir is removed wholesale on cache close.
	os.Remove(path)
	return f, size, nil
}

// Blob is a random-access handle to one cached blob. It follows io.ReaderAt
// to the letter: a short read always carries a non-nil error, reading at or
// past Size returns 0, io.EOF, and ReadAt calls may run concurrently.
type Blob struct {
	entry *cacheEntry
}

// ReadAt reads len(p) bytes from the blob at off.
func (b *Blob) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= b.entry.size {
		if off < 0 {
			return 0, fmt.Errorf("tiesfs: negative read offset %d", off)
		}
		return 0, io.EOF
	}
	n, err := b.entry.file.ReadAt(p, off)
	return n, err
}

// Size returns the blob's length in bytes.
func (b *Blob) Size() int64 { return b.entry.size }

// memberKey is the cache key for one extracted archive member. The archive hash
// is hex and the member name cannot contain a NUL, so the join is unambiguous.
func MemberKey(archiveHash, member string) string {
	return archiveHash + "\x00" + member
}

// ArchiveMembers downloads the archive blob into the cache (once, single-flight)
// and lists its members from the seekable cached file.
func (c *Cache) ArchiveMembers(archiveHash string) ([]archivelib.Member, error) {
	if !metadata.IsHexHash(archiveHash) {
		return nil, fmt.Errorf("tiesfs: invalid content hash %q", archiveHash)
	}
	e, err := c.get(archiveHash, func() (*os.File, int64, error) { return c.download(archiveHash) })
	if err != nil {
		return nil, err
	}
	// A SectionReader gives an independent, zero-based view of the shared cache
	// fd, so concurrent listings/reads don't race on its offset.
	return archivelib.List(io.NewSectionReader(e.file, 0, e.size))
}

// Member extracts one archive member into its own cache entry and returns a
// random-access handle to it. Zip members may be deflate-compressed, which is
// not seekable, so the member is decompressed to a cache file up front; reads
// then use random-access ReadAt like any other blob. Bounded by disk, not RAM.
func (c *Cache) Member(archiveHash, member string) (*Blob, error) {
	e, err := c.get(MemberKey(archiveHash, member), func() (*os.File, int64, error) {
		ae, err := c.get(archiveHash, func() (*os.File, int64, error) { return c.download(archiveHash) })
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
	if err != nil {
		return nil, err
	}
	return &Blob{entry: e}, nil
}
