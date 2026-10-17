package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/uidbz/tie/internal/diskcache"
	"github.com/uidbz/tie/metadata"
)

// blobCache is the filehost's read cache: a size-bounded copy of blobs from the
// primary stores (BlobPath, typically slow storage) into a faster directory
// (CachePath, e.g. an SSD or tmpfs).
//
// Storage, the byte budget, LRU eviction and handle refcounting are
// internal/diskcache's — the same engine the tiefs blob cache uses. This type
// supplies the producer (a copy out of the store) and makes the cache
// restart-aware: a CachePath reused across restarts is indexed at start-up, so
// what a previous run left behind still counts toward the budget and can be
// evicted. Before that the entry map started empty every run and the directory
// grew without bound.
type blobCache struct {
	dc *diskcache.Cache
}

// newBlobCache prepares the cache directory and adopts whatever a previous run
// left in it.
func newBlobCache(dir string, maxSizeBytes int64) (*blobCache, error) {
	dc, err := diskcache.New(diskcache.Options{
		Dir:      dir,
		MaxBytes: maxSizeBytes,
		// The cache mirrors the store layout, and PathFromHash doubles as key
		// validation: a hash that is not 64 hex never reaches the filesystem.
		Layout: PathFromHash,
	})
	if err != nil {
		return nil, err
	}
	c := &blobCache{dc: dc}
	c.adopt()
	return c, nil
}

// adopt indexes the cache directory, oldest-modified first so the LRU order
// reflects real recency rather than walk order.
func (c *blobCache) adopt() {
	type found struct {
		hash string
		path string
		size int64
		mod  time.Time
	}
	var items []found
	var total int64
	filepath.WalkDir(c.dc.Dir(), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !metadata.IsHexHash(name) {
			return nil // a partial file from a killed run, or a foreign file
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		items = append(items, found{name, path, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if len(items) == 0 {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		c.dc.Adopt(it.hash, it.path, it.size)
	}
	// A previous run may have left more than the current budget holds (the
	// budget can shrink between runs). Come back inside it now rather than
	// waiting for the next upload to trigger eviction.
	c.dc.Trim()
	slog.Info("read cache adopted", "entries", len(items), "bytes", total)
}

// peek returns a handle to an already-cached blob. It never starts a copy, so
// the request path can decide whether to answer from the store instead.
func (c *blobCache) peek(hash string) (*diskcache.Handle, bool) {
	return c.dc.Peek(hash)
}

// fill copies a blob into the cache in the background (single-flight), so the
// request that triggered it is not held up by the copy.
func (c *blobCache) fill(hash, srcPath string) {
	c.dc.Fill(hash, func(w io.Writer) error {
		f, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}

// Close releases the cache's handles. The files stay on disk for the next run
// to adopt.
func (c *blobCache) Close() error { return c.dc.Close() }
