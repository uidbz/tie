package fuselib

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	stdhash "hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"git.sr.ht/~uid/tie/metadata"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type TieFuse struct {
	inodes      uint64
	hashToInode map[string]uint64
	inodeLock   sync.RWMutex
	cache       *cache
	config      config
}

// Close releases the on-disk blob cache. Call once after the mount is torn down.
func (state *TieFuse) Close() error {
	return state.cache.close()
}

// NewTieFuse builds a content-addressed FUSE state. verify controls whether
// downloaded blob bytes are hashed and checked against their content address:
// off by default, since for a trusted personal filehost the per-read hash pass
// over multi-GB media is wasted work; turn it on when the filehost is untrusted.
func NewTieFuse(filehost string, insecure bool, cacheSizeGB int, verify bool) *TieFuse {
	httpClient := http.DefaultClient
	if insecure {
		httpClient = &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}
	}
	return &TieFuse{
		cache:       newCache(filehost, httpClient, int64(cacheSizeGB)*1024*1024*1024, verify),
		hashToInode: make(map[string]uint64),
		config:      config{filehost: filehost, client: httpClient, verify: verify},
	}
}

type config struct {
	filehost string
	client   *http.Client
	verify   bool
}

// cache is a disk-backed, single-flight blob cache. Blobs are streamed from the
// filehost to files under dir (bounded by disk, not RAM), verified against their
// content hash while streaming, and served to reads via ReadAt. Concurrent reads
// of the same hash — go-fuse dispatches Read across goroutines — coalesce into a
// single download.
type cache struct {
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

func newCache(filehost string, client *http.Client, maxSize int64, verify bool) *cache {
	dir, err := os.MkdirTemp("", "tie-fuse-cache-")
	if err != nil {
		// Fall back to the OS temp root; download() will surface open errors.
		dir = os.TempDir()
	}
	return &cache{
		filehost: filehost,
		client:   client,
		dir:      dir,
		maxSize:  maxSize,
		verify:   verify,
		entries:  make(map[string]*cacheEntry),
	}
}

// close removes the on-disk cache directory and closes open blob handles. Safe
// to call once at unmount.
func (c *cache) close() error {
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
func (c *cache) blobPath(hash string) string {
	return filepath.Join(c.dir, hash)
}

// evictLocked drops least-recently-inserted blobs until curSize fits maxSize.
// The open fd of an evicted entry is closed and its file unlinked; any reader
// still holding a reference to that entry keeps reading through its own fd.
// Must be called with c.mu held.
func (c *cache) evictLocked(keep string) {
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

func (state *TieFuse) inodeID(hash string) uint64 {
	state.inodeLock.RLock()
	i, ok := state.hashToInode[hash]
	state.inodeLock.RUnlock()
	if ok {
		return i
	}

	state.inodeLock.Lock()
	defer state.inodeLock.Unlock()

	state.inodes++
	state.hashToInode[hash] = state.inodes

	return state.inodes
}

// Ensure we are implementing the NodeReaddirer interface
var _ = (fs.NodeReaddirer)((*node)(nil))

// Readdir is part of the NodeReaddirer interface
func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	n, err := n.State.listContent(n.Hash)
	if err != nil {
		return nil, syscall.ENOENT
	}
	r := make([]fuse.DirEntry, 0, len(n.Children))

	for _, x := range n.Children {
		d := fuse.DirEntry{
			Name: x.Name,
			Ino:  n.State.inodeID(x.Hash),
			Mode: x.Mode,
		}
		r = append(r, d)
	}
	return fs.NewListDirStream(r), 0
}

// Ensure we are implementing the NodeLookuper interface
var _ = (fs.NodeLookuper)((*node)(nil))

// Lookup is part of the NodeLookuper interface
func (current *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n, err := current.State.listContent(current.Hash)
	if err != nil {
		return nil, syscall.ENOENT
	}
	var child *node
	for _, x := range n.Children {
		if x.Name == name {
			child = x
			break
		}
	}
	if child == nil {
		return nil, syscall.ENOENT
	}

	stable := fs.StableAttr{
		Mode: child.Mode,
		// The child inode is identified by its Inode number.
		// If multiple concurrent lookups try to find the same
		// inode, they are deduplicated on this key.
		Ino: current.State.inodeID(child.Hash),
	}
	operations := &node{Hash: child.Hash, Size: child.Size, State: current.State} // The Open function is run on this

	// fmt.Println("OP:", operations.Hash, operations.Size)
	// The NewInode call wraps the `operations` object into an Inode.
	c := current.NewInode(ctx, operations, stable)

	// In case of concurrent lookup requests, it can happen that operations !=
	// child.Operations().
	return c, 0
}

var _ = (fs.NodeOpener)((*node)(nil))

// Getattr sets the minimum, which is the size. A more full-featured
// FS would also set timestamps and permissions.
var _ = (fs.NodeGetattrer)((*node)(nil))

func (n *node) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Size = uint64(n.Size)
	// Content-addressed nodes are immutable: directories get 0555 (r-xr-xr-x),
	// files get 0444 (r--r--r--).
	if n.Mode&fuse.S_IFDIR != 0 {
		out.Mode = 0555 | fuse.S_IFDIR
	} else {
		out.Mode = 0444 | fuse.S_IFREG
	}
	return 0
}

func (f *node) Open(ctx context.Context, openFlags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	// disallow writes
	if fuseFlags&(syscall.O_RDWR|syscall.O_WRONLY) != 0 {
		return nil, 0, syscall.EROFS
	}

	fh = &bytesFileHandle{
		hash:  f.Hash,
		cache: f.State.cache,
	}

	// Return FOPEN_DIRECT_IO if content should not be cached.
	// return fh, fuse.FOPEN_DIRECT_IO, fs.OK
	return fh, fuse.FOPEN_KEEP_CACHE, fs.OK
}

// bytesFileHandle is a file handle that serves one blob's bytes from the cache.
type bytesFileHandle struct {
	hash  string
	cache *cache
}

// blob returns the ready cache entry for hash, downloading it on the first
// request and coalescing concurrent requests for the same hash into that single
// download. The returned entry's file is an open, read-only handle positioned by
// ReadAt; entry.err is set if the download failed.
func (c *cache) blob(hash string) (*cacheEntry, error) {
	if !metadata.IsHexHash(hash) {
		return nil, fmt.Errorf("fuselib: invalid content hash %q", hash)
	}

	c.mu.Lock()
	if e, ok := c.entries[hash]; ok {
		c.mu.Unlock()
		<-e.ready
		return e, e.err
	}
	e := &cacheEntry{ready: make(chan struct{})}
	c.entries[hash] = e
	c.mu.Unlock()

	// This goroutine won the race to create the entry, so it owns the download.
	e.file, e.size, e.err = c.download(hash)
	if e.err != nil {
		// Drop the failed entry so a later read can retry rather than caching
		// the error forever.
		c.mu.Lock()
		delete(c.entries, hash)
		c.mu.Unlock()
		close(e.ready)
		return nil, e.err
	}

	c.mu.Lock()
	c.order = append(c.order, hash)
	c.curSize += e.size
	c.evictLocked(hash)
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
func (c *cache) download(hash string) (*os.File, int64, error) {
	resp, err := c.client.Get(c.filehost + "/" + hash)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("fuselib: bad status %s", resp.Status)
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
			return nil, 0, errors.New("fuselib: downloaded content does not match its hash")
		}
	}

	// Unlink now: the open fd keeps the bytes reachable, and eviction/close no
	// longer needs a second unlink. The dir is removed wholesale on cache close.
	os.Remove(path)
	return f, size, nil
}

// bytesFileHandle allows reads
var _ = (fs.FileReader)((*bytesFileHandle)(nil))

func (fh *bytesFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	e, err := fh.cache.blob(fh.hash)
	if err != nil {
		return nil, syscall.EIO
	}
	if off < 0 || off > e.size {
		return nil, syscall.EINVAL
	}
	end := off + int64(len(dest))
	if end > e.size {
		end = e.size
	}
	n, err := e.file.ReadAt(dest[:end-off], off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

type node struct {
	// Must embed an Inode for the struct to work as a node.
	fs.Inode

	Hash     string
	Name     string
	Mode     uint32  // file (fuse.S_IFREG) or directory (fuse.S_IFDIR)
	Children []*node // for directories
	Size     int
	State    *TieFuse
}

func (n *node) AddChild(child *node) {
	n.Children = append(n.Children, child)
}

const dirHeader = metadata.DirHeader

func (state *TieFuse) listContent(sourceHash string) (*node, error) {
	if !metadata.IsHexHash(sourceHash) {
		return nil, fmt.Errorf("fuselib: invalid content hash %q", sourceHash)
	}
	resp, err := state.config.client.Get(state.config.filehost + "/" + sourceHash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// When verification is enabled, check the blob against its hash before
	// trusting its bytes (the filehost is untrusted). Off by default.
	if state.config.verify {
		got, err := metadata.HashReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if got != sourceHash {
			return nil, errors.New("fuselib: content does not match its hash")
		}
	}

	n := &node{
		Hash:  sourceHash,
		State: state,
	}

	isDir := len(body) >= len(dirHeader) && string(body[:len(dirHeader)]) == dirHeader
	if isDir {
		n.Mode = fuse.S_IFDIR
		scanner := bufio.NewScanner(bytes.NewReader(body))
		for scanner.Scan() {
			entry, ok := metadata.ParseDirLine(scanner.Text())
			if !ok {
				continue
			}
			child := &node{
				Hash:  entry.Hash,
				Name:  entry.Filename,
				Size:  entry.Size,
				State: state,
			}
			if metadata.IsDirHead(entry.Head) {
				child.Mode = fuse.S_IFDIR
			} else {
				child.Mode = fuse.S_IFREG
			}
			n.AddChild(child)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	} else {
		n.Mode = fuse.S_IFREG
	}

	return n, nil
}

func (state *TieFuse) Mount(hash, mountpoint string) (*fuse.Server, error) {
	root, err := state.listContent(hash)
	if err != nil {
		return nil, err
	}

	server, err := fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			// Set to true to see how the file system works.
			Debug: false,
		},
	})
	if err != nil {
		return nil, err
	}

	return server, nil
}
