package main

// blobCache is a disk-backed LRU cache that copies blobs from the primary blob
// store (BlobPath, typically slow storage) into a faster directory (CachePath,
// e.g. an SSD or tmpfs). It mirrors the cache design in io/fuselib/fuselib.go:
//
//   - A per-entry ready channel coalesces concurrent requests for the same hash
//     into a single copy, so the blob is read from slow storage exactly once.
//   - The size budget is enforced by LRU eviction (insertion order, like fuselib).
//     A blob larger than the budget is still cached; it is only evicted when
//     another blob is admitted that would push curSize over maxSize.
//   - Files are kept on disk (not unlinked after open) so that http.ServeFile can
//     open them by path for every request. There is a narrow race: if a cached
//     file is evicted between get() returning and http.ServeFile opening it, the
//     client gets a 404-ish error for that one request. In practice the window is
//     nanoseconds and the client can retry; content-addressed blobs are always
//     available from BlobPath as a fallback.

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

type blobCache struct {
	srcDir  string // primary blob store (BlobPath)
	dir     string // cache directory (CachePath, faster storage)
	maxSize int64  // soft size budget in bytes

	// mu guards entries, order, and curSize. Copies happen outside the lock;
	// per-entry ready channels signal completion to waiting goroutines.
	mu      sync.Mutex
	entries map[string]*blobCacheEntry
	order   []string // hashes in insertion order; front = oldest (evict first)
	curSize int64
}

// blobCacheEntry tracks one cached blob. ready is closed once the copy is done
// (or has failed). On success path holds the on-disk location in the cache dir
// and size is the byte count; on failure err is set and the entry is removed so
// the next request can retry.
type blobCacheEntry struct {
	ready chan struct{}
	path  string
	size  int64
	err   error
}

// newBlobCache creates a blobCache that copies blobs from srcDir into dir,
// enforcing a soft LRU budget of maxSizeBytes. dir is created if absent.
func newBlobCache(srcDir, dir string, maxSizeBytes int64) (*blobCache, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("blob cache: creating cache dir %q: %w", dir, err)
	}
	return &blobCache{
		srcDir:  srcDir,
		dir:     dir,
		maxSize: maxSizeBytes,
		entries: make(map[string]*blobCacheEntry),
	}, nil
}

// blobPath returns the path where hash is stored inside the cache dir.
// Blobs are stored flat (no sharding) because the cache dir is separate from
// the primary store and may only hold a subset of blobs.
func (c *blobCache) blobPath(hash string) string {
	return filepath.Join(c.dir, hash)
}

// get ensures hash is present in the cache dir and returns its path. On the
// first call for a hash it copies from srcDir; concurrent callers for the same
// hash block until that single copy completes and then reuse the result.
func (c *blobCache) get(hash string) (string, error) {
	c.mu.Lock()
	if e, ok := c.entries[hash]; ok {
		c.mu.Unlock()
		<-e.ready // wait for the in-progress copy to finish
		return e.path, e.err
	}
	e := &blobCacheEntry{ready: make(chan struct{})}
	c.entries[hash] = e
	c.mu.Unlock()

	// This goroutine won the race; it owns the copy.
	e.path, e.size, e.err = c.copyBlob(hash)
	if e.err != nil {
		// Drop the failed entry so a later request can retry.
		c.mu.Lock()
		delete(c.entries, hash)
		c.mu.Unlock()
		close(e.ready)
		return "", e.err
	}

	c.mu.Lock()
	c.order = append(c.order, hash)
	c.curSize += e.size
	c.evictLocked(hash)
	c.mu.Unlock()

	close(e.ready)
	return e.path, nil
}

// copyBlob copies hash from the primary blob store into the cache dir and
// returns the new path and the byte count. Memory use is bounded by the copy
// buffer (~32 KB), not the blob size.
func (c *blobCache) copyBlob(hash string) (string, int64, error) {
	src := PathFromHash(c.srcDir, hash)
	in, err := os.Open(src)
	if err != nil {
		return "", 0, fmt.Errorf("blob cache: opening source %q: %w", src, err)
	}
	defer in.Close()

	dst := c.blobPath(hash)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return "", 0, fmt.Errorf("blob cache: creating cache file %q: %w", dst, err)
	}

	n, err := io.Copy(out, in)
	out.Close()
	if err != nil {
		os.Remove(dst)
		return "", 0, fmt.Errorf("blob cache: copying %q: %w", hash, err)
	}
	slog.Debug("blob cached", "hash", hash, "bytes", n)
	return dst, n, nil
}

// evictLocked drops the oldest blobs until curSize fits within maxSize.
// keep is the hash just admitted; it is rotated to the back rather than evicted
// immediately (a blob larger than the budget is still served rather than looped).
// Must be called with c.mu held.
func (c *blobCache) evictLocked(keep string) {
	for c.curSize > c.maxSize && len(c.order) > 0 {
		oldest := c.order[0]
		if oldest == keep {
			// Never evict the blob we just admitted.
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
		os.Remove(e.path)
		slog.Debug("blob evicted from cache", "hash", oldest)
	}
}

// close removes all cached files and clears the in-memory state. It does not
// delete the cache directory itself (it is user-configured and may be reused
// across restarts).
func (c *blobCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for hash, e := range c.entries {
		os.Remove(e.path)
		delete(c.entries, hash)
	}
	c.order = nil
	c.curSize = 0
}
