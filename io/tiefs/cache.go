package tiesfs

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	stdhash "hash"

	"github.com/uidbz/tie/internal/diskcache"
	"github.com/uidbz/tie/io/archivelib"
	"github.com/uidbz/tie/metadata"
)

// Cache is the shared read path for every tie transport (FUSE mount, SMB
// server): a blob is downloaded once no matter how many protocol handles ask
// for it, streamed to disk (bounded by disk, not RAM), verified against its
// content address while streaming when the filehost is untrusted, and served
// to reads through random-access ReadAt.
//
// Storage, the size budget, LRU eviction and handle refcounting live in
// internal/diskcache — the same engine the filehost read cache uses. This type
// only supplies the producers: an HTTP fetch, or extracting one archive member.
type Cache struct {
	filehost string
	client   *http.Client
	verify   bool // hash downloaded bytes and check against their content address

	dc *diskcache.Cache
	// newErr records a failure to create the cache directory; every read then
	// reports it instead of dereferencing a nil cache.
	newErr error
}

// NewCache builds a blob cache reading from filehostURL with the given HTTP
// client. maxBytes is the LRU eviction budget; verify controls whether
// downloaded bytes are hashed and checked against their content address (off
// for a trusted filehost, on when the filehost is untrusted).
func NewCache(filehostURL string, client *http.Client, maxBytes int64, verify bool) *Cache {
	c := &Cache{filehost: filehostURL, client: client, verify: verify}
	dir, err := os.MkdirTemp("", "tie-cache-")
	if err != nil {
		c.newErr = fmt.Errorf("tiesfs: creating cache directory: %w", err)
		return c
	}
	dc, err := diskcache.New(diskcache.Options{Dir: dir, MaxBytes: maxBytes, Layout: cacheFileName})
	if err != nil {
		os.RemoveAll(dir)
		c.newErr = err
		return c
	}
	c.dc = dc
	return c
}

// cacheFileName maps a cache key to a file name under dir. Blob keys are
// already 64-hex; archive-member keys are "hash\x00member" and are hashed into
// a hex name so they stay single, safe path segments.
func cacheFileName(dir, key string) (string, error) {
	name := key
	if !metadata.IsHexHash(name) {
		h, err := metadata.HashReader(strings.NewReader(key))
		if err != nil {
			return "", fmt.Errorf("tiesfs: cache key %q: %w", key, err)
		}
		name = h
	}
	return filepath.Join(dir, name), nil
}

// Close removes the on-disk cache directory. Safe to call once at shutdown.
func (c *Cache) Close() error {
	if c.dc == nil {
		return nil
	}
	dir := c.dc.Dir()
	c.dc.Close()
	return os.RemoveAll(dir)
}

// get is the single entry point into the shared cache, so a cache that failed
// to create its directory reports that rather than panicking.
func (c *Cache) get(key string, produce func(io.Writer) error) (*diskcache.Handle, error) {
	if c.dc == nil {
		return nil, c.newErr
	}
	return c.dc.Get(key, produce)
}

// fetch streams one blob from the filehost into w, hashing as it copies when
// verification is on. Memory use is bounded by the copy buffer, not the blob
// size, so blobs larger than RAM (or the cache budget) serve fine.
func (c *Cache) fetch(hash string, w io.Writer) error {
	resp, err := c.client.Get(c.filehost + "/" + hash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tiesfs: bad status %s", resp.Status)
	}

	var dst io.Writer = w
	var h stdhash.Hash
	if c.verify {
		h, err = metadata.NewHash()
		if err != nil {
			return err
		}
		dst = io.MultiWriter(w, h)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		return err
	}
	if c.verify {
		if got := hex.EncodeToString(h.Sum(nil)); got != hash {
			return errors.New("tiesfs: downloaded content does not match its hash")
		}
	}
	return nil
}

// Blob returns a random-access handle to the blob for hash, downloading it on
// the first request and coalescing concurrent requests for the same hash into
// that single download. The caller must Close it.
func (c *Cache) Blob(hash string) (*Blob, error) {
	if !metadata.IsHexHash(hash) {
		return nil, fmt.Errorf("tiesfs: invalid content hash %q", hash)
	}
	h, err := c.get(hash, func(w io.Writer) error { return c.fetch(hash, w) })
	if err != nil {
		return nil, err
	}
	return &Blob{h: h}, nil
}

// Blob is a random-access handle to one cached blob. It follows io.ReaderAt
// to the letter: a short read always carries a non-nil error, reading at or
// past Size returns 0, io.EOF, and ReadAt calls may run concurrently.
//
// The handle holds a reference on its cache entry: the entry may be evicted
// while the handle is live, but it stays readable until Close. Handles that
// are never closed pin the blob's bytes, so every Blob must be released when
// the protocol handle using it goes away.
type Blob struct {
	h *diskcache.Handle
}

// ReadAt reads len(p) bytes from the blob at off.
func (b *Blob) ReadAt(p []byte, off int64) (int, error) { return b.h.ReadAt(p, off) }

// Size returns the blob's length in bytes.
func (b *Blob) Size() int64 { return b.h.Size() }

// Close releases this handle's reference on the cached blob.
func (b *Blob) Close() error { return b.h.Close() }

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
	h, err := c.get(archiveHash, func(w io.Writer) error { return c.fetch(archiveHash, w) })
	if err != nil {
		return nil, err
	}
	defer h.Close()
	return archivelib.List(h.Section())
}

// Member extracts one archive member into its own cache entry and returns a
// random-access handle to it. Zip members may be deflate-compressed, which is
// not seekable, so the member is decompressed to a cache file up front; reads
// then use random-access ReadAt like any other blob. Bounded by disk, not RAM.
func (c *Cache) Member(archiveHash, member string) (*Blob, error) {
	h, err := c.get(MemberKey(archiveHash, member), func(w io.Writer) error {
		ah, err := c.get(archiveHash, func(w io.Writer) error { return c.fetch(archiveHash, w) })
		if err != nil {
			return err
		}
		defer ah.Close()
		rc, err := archivelib.Open(ah.Section(), member)
		if err != nil {
			return err
		}
		defer rc.Close()
		_, err = io.Copy(w, rc)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Blob{h: h}, nil
}
