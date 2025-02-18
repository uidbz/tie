package fuselib

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

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

func NewTieFuse(filehost string, cacheSizeGB int) *TieFuse {
	return &TieFuse{
		cache:       &cache{MaxSize: cacheSizeGB * 1024 * 1024 * 1024, filehost: filehost},
		hashToInode: make(map[string]uint64),
		config:      config{filehost: filehost},
	}
}

type config struct {
	filehost string
}

type cache struct {
	filehost    string
	CurrentSize int
	MaxSize     int
	Entries     []*cacheEntry
}

func (c *cache) set(key string, data []byte) error {
	if len(data) > c.MaxSize {
		return errors.New("File bigger than cache")
	}
	entry := &cacheEntry{key, data}
	c.Entries = append(c.Entries, entry)
	c.CurrentSize += len(entry.Data)
	for c.CurrentSize > c.MaxSize {
		var x *cacheEntry
		x, c.Entries = c.Entries[0], c.Entries[1:]
		c.CurrentSize = c.CurrentSize - len(x.Data)
	}
	return nil
}

func (c *cache) get(key string) ([]byte, bool) {
	for _, x := range c.Entries {
		if x.Key == key {
			return x.Data, true
		}
	}
	return nil, false
}

type cacheEntry struct {
	Key  string
	Data []byte
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

// bytesFileHandle is a file handle that carries separate content for
// each Open call
type bytesFileHandle struct {
	hash  string
	cache *cache
}

func (c *cache) download(hash string) ([]byte, error) {
	resp, err := http.Get(c.filehost + "/" + hash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("Bad status: " + strconv.Itoa(resp.StatusCode))
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	err = c.set(hash, b)
	if err != nil {
		return nil, err
	}

	return b, nil
}

// bytesFileHandle allows reads
var _ = (fs.FileReader)((*bytesFileHandle)(nil))

func (fh *bytesFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	b, ok := fh.cache.get(fh.hash)
	if !ok {
		var err error
		b, err = fh.cache.download(fh.hash)
		if err != nil {
			return nil, syscall.EROFS
		}
	}

	end := off + int64(len(dest))
	if end > int64(len(b)) {
		end = int64(len(b))
	}

	if off > end {
		return nil, syscall.EROFS
	}

	// We could copy to the `dest` buffer, but since we have a
	// []byte already, return that.
	return fuse.ReadResultData(b[off:end]), 0
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

const dirHeader = "tiedir-v1\n---\n"

func (state *TieFuse) listContent(sourceHash string) (*node, error) {
	resp, err := http.Get(state.config.filehost + "/" + sourceHash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	var buf bytes.Buffer
	io.CopyN(&buf, resp.Body, int64(len(dirHeader)))
	mode := string(buf.Bytes())
	_, err = io.Copy(&buf, resp.Body)
	if err != nil {
		return nil, err
	}

	n := &node{
		Hash:  sourceHash,
		State: state,
	}

	if mode == dirHeader {
		n.Mode = fuse.S_IFDIR
		scanner := bufio.NewScanner(&buf)
		for scanner.Scan() {
			parts := strings.Split(scanner.Text(), "\t")
			if len(parts) == 4 { // skips dir and ---
				child := &node{
					Hash:  parts[0],
					Name:  filepath.Base(parts[1]),
					State: state,
				}
				if parts[2] == "inode/directory" {
					child.Mode = fuse.S_IFDIR
				} else {
					child.Mode = fuse.S_IFREG
				}
				child.Size, _ = strconv.Atoi(parts[3])
				n.AddChild(child)
			}
		}
		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
	} else {
		n.Mode = fuse.S_IFREG
	}

	return n, nil
}

func (state *TieFuse) Mount(hash, mountpoint string) *fuse.Server {
	root, err := state.listContent(hash)
	if err != nil {
		panic(err)
	}

	server, err := fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			// Set to true to see how the file system works.
			Debug: false,
		},
	})
	if err != nil {
		log.Panic(err)
	}

	return server
}
