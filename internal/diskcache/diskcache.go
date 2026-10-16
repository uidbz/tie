// Package diskcache is a size-bounded, single-flight, refcounted disk cache.
//
// It exists because the tiefs blob cache (fetch over HTTP, extract archive
// members) and the filehost read cache (copy from slow storage) were the same
// design written twice — same ready-channel coalescing, same FIFO eviction,
// and the same class of bug fixed twice in divergent ways (closing an fd a
// live reader holds, evicting the hottest entry first, growing past the
// budget across a restart).
//
// A Cache stores objects under a directory using a caller-supplied layout,
// keeps them under a byte budget with true LRU eviction, and hands out
// refcounted *Handle values that stay readable even if the entry is evicted
// underneath them.
package diskcache

import (
	"container/list"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// ErrNotFound is returned by Peek for a key that is absent or still filling.
var ErrNotFound = errors.New("diskcache: not cached")

// Options configures a Cache.
type Options struct {
	// Dir is the cache directory; it is created if absent.
	Dir string
	// MaxBytes is the soft size budget. A single object larger than the
	// budget is still cached (and served); eviction simply cannot make room
	// for anything else until it goes.
	MaxBytes int64
	// Layout maps a key to its path under Dir. It doubles as key validation:
	// returning an error rejects the key. nil means Dir/<key>, which is only
	// safe for keys the caller has already validated as single path segments.
	Layout func(dir, key string) (string, error)
}

// Cache is one disk cache.
type Cache struct {
	dir     string
	maxSize int64
	layout  func(dir, key string) (string, error)

	// mu guards entries, the LRU list and curSize. Producers run outside it;
	// each entry carries its own readiness channel.
	mu      sync.Mutex
	entries map[string]*entry
	order   *list.List // front = least recently used, back = most recent
	curSize int64
	closed  bool
}

// entry is one cached object. ready closes once produce finishes (or fails);
// until then other getters block on it. file is the open read handle that
// makes ReadAt work without reopening, and it stays open while refs > 0 even
// after eviction unlinks the name.
type entry struct {
	ready chan struct{}
	path  string
	size  int64
	err   error

	refs    int
	evicted bool
	elem    *list.Element

	file *os.File
}

// New prepares a cache. With Options.Adopt the caller indexes whatever is
// already in Dir (see Adopt) so the budget survives a restart.
func New(opts Options) (*Cache, error) {
	if opts.Dir == "" {
		return nil, errors.New("diskcache: empty Dir")
	}
	if err := os.MkdirAll(opts.Dir, 0755); err != nil {
		return nil, fmt.Errorf("diskcache: creating %q: %w", opts.Dir, err)
	}
	layout := opts.Layout
	if layout == nil {
		layout = func(dir, key string) (string, error) { return dir + "/" + key, nil }
	}
	return &Cache{
		dir:     opts.Dir,
		maxSize: opts.MaxBytes,
		layout:  layout,
		entries: make(map[string]*entry),
		order:   list.New(),
	}, nil
}

// Dir returns the cache directory.
func (c *Cache) Dir() string { return c.dir }

// Path returns where key lives (or will live) in the cache directory.
func (c *Cache) Path(key string) (string, error) { return c.layout(c.dir, key) }

// Adopt records an object that is already on disk under Dir, so a cache that
// reuses a directory across restarts counts and evicts what a previous run
// left behind instead of growing past the budget forever. Call it during
// start-up, before serving, walking the directory oldest-mtime-first so the
// LRU order reflects real recency.
func (c *Cache) Adopt(key, path string, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.entries[key] != nil {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	e := &entry{ready: make(chan struct{}), path: path, size: size, file: f}
	close(e.ready)
	c.entries[key] = e
	e.elem = c.order.PushBack(key)
	c.curSize += size
}

// Get returns a refcounted handle for key. The first request for a key runs
// produce, which writes the object's bytes to the cache file; concurrent
// requests for the same key wait for that single run and share its result.
// A failed produce is dropped, so a later request can retry.
func (c *Cache) Get(key string, produce func(io.Writer) error) (*Handle, error) {
	e := c.start(key, produce)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("diskcache: cache closed")
	}
	if e.err != nil {
		c.mu.Unlock()
		return nil, e.err
	}
	// Take the reference under the lock: an eviction starting after this
	// point sees refs > 0 and cannot close the fd under the caller.
	e.refs++
	if e.elem != nil {
		c.order.MoveToBack(e.elem)
	}
	c.mu.Unlock()
	return &Handle{c: c, e: e}, nil
}

// Fill starts producing key in the background and returns immediately. It is
// the serve-while-filling half of the filehost's read path: the request is
// answered from the source while the cache fills behind it.
func (c *Cache) Fill(key string, produce func(io.Writer) error) {
	go func() {
		e := c.start(key, produce)
		<-e.ready
	}()
}

// Peek returns a handle only if key is already cached and ready. It never
// starts a produce.
func (c *Cache) Peek(key string) (*Handle, bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, false
	}
	e := c.entries[key]
	if e == nil {
		c.mu.Unlock()
		return nil, false
	}
	select {
	case <-e.ready:
		// The close of ready happens after the producer wrote err/size/path,
		// so reading them here is ordered.
	default:
		c.mu.Unlock()
		return nil, false // still filling
	}
	if e.err != nil {
		c.mu.Unlock()
		return nil, false
	}
	e.refs++
	if e.elem != nil {
		c.order.MoveToBack(e.elem)
	}
	c.mu.Unlock()
	return &Handle{c: c, e: e}, true
}

// start returns the entry for key, creating it and running produce if this
// caller won the race. Waiting callers get the same entry and block on ready.
func (c *Cache) start(key string, produce func(io.Writer) error) *entry {
	c.mu.Lock()
	if c.closed {
		e := &entry{ready: make(chan struct{}), err: errors.New("diskcache: cache closed")}
		close(e.ready)
		c.mu.Unlock()
		return e
	}
	if e, ok := c.entries[key]; ok {
		c.mu.Unlock()
		<-e.ready
		return e
	}
	e := &entry{ready: make(chan struct{})}
	c.entries[key] = e
	c.mu.Unlock()

	// This goroutine owns the work.
	e.path, e.size, e.file, e.err = c.produceInto(key, produce)
	if e.err != nil {
		c.mu.Lock()
		if c.entries[key] == e {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		close(e.ready)
		return e
	}

	c.mu.Lock()
	e.elem = c.order.PushBack(key)
	c.curSize += e.size
	c.evictLocked(key)
	c.mu.Unlock()

	close(e.ready)
	return e
}

// produceInto opens the destination file for key, runs produce into it, and
// returns the path and byte count. Memory use is bounded by the copy buffer,
// not the object size.
func (c *Cache) produceInto(key string, produce func(io.Writer) error) (string, int64, *os.File, error) {
	path, err := c.layout(c.dir, key)
	if err != nil {
		return "", 0, nil, err
	}
	if err := mkdirFor(path); err != nil {
		return "", 0, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", 0, nil, fmt.Errorf("diskcache: creating %q: %w", path, err)
	}
	n, perr := func() (int64, error) {
		defer f.Close()
		return writeThrough(f, produce)
	}()
	if perr != nil {
		os.Remove(path)
		return "", 0, nil, perr
	}
	// Keep the file (a restart may reuse it) and keep an open read handle so
	// ReadAt never has to reopen.
	rf, err := os.Open(path)
	if err != nil {
		return "", 0, nil, fmt.Errorf("diskcache: reopening %q: %w", path, err)
	}
	return path, n, rf, nil
}

// writeThrough runs produce against w, returning the byte count. produce may
// write through several writers (the cache uses MultiWriter for verification).
func writeThrough(w io.Writer, produce func(io.Writer) error) (int64, error) {
	if err := produce(w); err != nil {
		return 0, err
	}
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			return info.Size(), nil
		}
	}
	return 0, nil
}

// evictLocked drops least-recently-used entries until curSize fits the budget.
// An evicted entry leaves the map and the list, unlinks its file, and closes
// its fd only when no handle is still reading through it. Callers hold mu.
func (c *Cache) evictLocked(keep string) {
	for c.curSize > c.maxSize {
		el := c.order.Front()
		if el == nil {
			return
		}
		key := el.Value.(string)
		if key == keep {
			if c.order.Len() == 1 {
				return // one oversized object: serve it, don't churn
			}
			c.order.MoveToBack(el)
			continue
		}
		c.order.Remove(el)
		e := c.entries[key]
		if e == nil {
			continue
		}
		delete(c.entries, key)
		c.curSize -= e.size
		e.evicted = true
		if e.refs <= 0 {
			e.closeFile()
		}
		os.Remove(e.path)
	}
}

// release drops one handle's reference; the fd closes when the last handle
// goes and the entry has already left the cache.
func (c *Cache) release(e *entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.refs > 0 {
		e.refs--
	}
	if e.refs == 0 && e.evicted {
		e.closeFile()
	}
}

func (e *entry) closeFile() {
	if e.file != nil {
		e.file.Close()
		e.file = nil
	}
}

// Close closes every open handle and leaves the files on disk (the cache
// directory is the caller's to remove).
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, e := range c.entries {
		e.closeFile()
	}
	c.entries = make(map[string]*entry)
	c.order = list.New()
	c.curSize = 0
	return nil
}

// Trim enforces the size budget now, without admitting anything. A cache that
// adopted a directory a previous run left behind uses this to come back inside
// its budget at start-up rather than only when the next object arrives.
func (c *Cache) Trim() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked("")
}

// Stats reports the cached byte total and entry count (for logging/tests).
func (c *Cache) Stats() (bytes int64, entries int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.curSize, len(c.entries)
}

// Handle is a refcounted random-access view of one cached object.
type Handle struct {
	c *Cache
	e *entry
}

// Size returns the object's length in bytes.
func (h *Handle) Size() int64 { return h.e.size }

// Path returns the on-disk location. It is valid while the entry is cached:
// eviction unlinks the name, so prefer ReadAt for anything that must survive
// an eviction mid-request.
func (h *Handle) Path() string { return h.e.path }

// ReadAt reads len(p) bytes at off, following io.ReaderAt semantics. It works
// through the entry's own fd, so an eviction during the read is harmless.
func (h *Handle) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("diskcache: negative read offset %d", off)
	}
	if off >= h.e.size {
		return 0, io.EOF
	}
	h.c.mu.Lock()
	f := h.e.file
	h.c.mu.Unlock()
	if f == nil {
		return 0, errors.New("diskcache: handle released")
	}
	return f.ReadAt(p, off)
}

// Section returns a seekable view of the whole object, for http.ServeContent
// and archive readers.
func (h *Handle) Section() *io.SectionReader {
	return io.NewSectionReader(h, 0, h.e.size)
}

// Close releases this handle's reference. Using the handle afterwards is a
// programming error.
func (h *Handle) Close() error {
	if h == nil || h.c == nil {
		return nil
	}
	h.c.release(h.e)
	h.c = nil
	return nil
}

// mkdirFor creates the parent directory of path.
func mkdirFor(path string) error {
	dir := path[:lastSlash(path)]
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("diskcache: creating %q: %w", dir, err)
	}
	return nil
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
