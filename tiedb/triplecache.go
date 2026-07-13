package tiedb

import (
	"container/list"
	"sync"
)

// defaultTripleCacheSize bounds the disk-mode triple cache. Each entry is a
// Triple (~48 B) plus list/map overhead, so 100k entries is a few MB.
const defaultTripleCacheSize = 100000

// tripleCache is a bounded LRU mapping a triple's on-disk position to its
// decoded Triple. It exists only in disk-backed mode, where associations keep
// just an int64 position resident; the cache spares a disk read for hot
// triples on repeated queries. Memory-only collections keep Triples resident
// and never use it.
type tripleCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	items    map[int64]*list.Element
}

type cacheEntry struct {
	pos int64
	t   Triple
}

func newTripleCache(capacity int) *tripleCache {
	if capacity <= 0 {
		capacity = defaultTripleCacheSize
	}
	return &tripleCache{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[int64]*list.Element),
	}
}

func (c *tripleCache) Get(pos int64) (Triple, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[pos]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*cacheEntry).t, true
	}
	return Triple{}, false
}

func (c *tripleCache) Put(pos int64, t Triple) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[pos]; ok {
		el.Value.(*cacheEntry).t = t
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&cacheEntry{pos: pos, t: t})
	c.items[pos] = el
	if c.ll.Len() > c.capacity {
		if oldest := c.ll.Back(); oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).pos)
		}
	}
}

func (c *tripleCache) Evict(pos int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[pos]; ok {
		c.ll.Remove(el)
		delete(c.items, pos)
	}
}
