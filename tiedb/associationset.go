package tiedb

import (
	"sync"
)

// assocShards is the number of independently-locked map shards a large
// AssociationSet is split across. It must be a power of two so the shard index
// is a cheap mask. Sharding lets concurrent load workers insert into the same
// large set (the reverse index's shared-value hot sets) without serializing on
// a single lock.
const assocShards = 16

// shardThreshold is the entry count at which the small-set slice splits into the
// sharded map representation. Below it a set keeps a single flat slice: a Go map
// carries bucket + control-word overhead even when nearly empty (~272 B for a
// 2-entry map vs ~88 B for a 2-entry slice), and the sharded array adds ~1 KiB
// of mutexes+headers on top. A linear scan over a slice this small is also as
// fast as hashing and stays in cache. The threshold is kept modest so the
// O(n) per-insert scan (O(n^2) to build a set) never dominates: a set headed for
// millions of entries (the reverse index's hot sets) crosses over to the sharded
// map after only ~shardThreshold serial inserts, then gets O(1) sharded writes
// with 16-way concurrency for the rest.
const shardThreshold = 256

// assocEntry is one (key, position) pair in the small-set slice.
type assocEntry struct {
	key UniqueAssociation
	pos int64
}

// AssociationSet is the inner association store: a mapping from
// UniqueAssociation to an int64 position (a disk offset in disk mode, or an
// arena index in memory mode). It replaces the former red-black tree: the query
// paths never use key ordering (Sort re-sorts by resolved strings,
// intersect/exclude use point lookups), so there is no need to keep entries
// ordered.
//
// Three representations, chosen by size:
//   - a single inline entry (no allocation) — the common forward-index case
//     where a content-address key holds exactly one association
//   - a flat slice — the small case (2..shardThreshold distinct keys), the most
//     memory-compact form for the bimodal middle population
//   - a sharded map — only once a set exceeds shardThreshold, for the reverse
//     index's shared-value hot sets that many load workers hammer concurrently
//
// It is internal to tiedb: association set algebra reaches callers only through
// Collection.QueryTags, so no map-of-generic type ever crosses into api/.
type AssociationSet struct {
	// changeLock guards the inline fields, the slice, and every tier transition.
	// Once shards is non-nil the set stays sharded and per-shard locks guard the
	// map contents; changeLock is then held only in read mode to observe the
	// stable shards pointer.
	changeLock sync.RWMutex

	inlineKey UniqueAssociation
	inlineVal int64
	hasInline bool

	small  []assocEntry   // small tier; guarded by changeLock
	shards *assocShardSet // large tier; nil until promoted
}

type assocShardSet struct {
	shards [assocShards]assocShard
}

type assocShard struct {
	mu sync.RWMutex
	m  map[UniqueAssociation]int64 // lazily allocated on first write
}

// newAssociationSet returns an empty lazy set.
func newAssociationSet() *AssociationSet {
	return &AssociationSet{}
}

// shardFor returns the shard a key belongs to. The mix spreads the two uint64
// fields across the low bits so keys that differ only in Relation still scatter.
func (ss *assocShardSet) shardFor(key UniqueAssociation) *assocShard {
	h := key.AssociateTo ^ (key.Relation * 0x9e3779b97f4a7c15)
	return &ss.shards[h&(assocShards-1)]
}

// promote splits the small slice into the sharded representation. Caller must
// hold changeLock for writing.
func (s *AssociationSet) promote() {
	s.shards = &assocShardSet{}
	for _, e := range s.small {
		sh := s.shards.shardFor(e.key)
		if sh.m == nil {
			sh.m = make(map[UniqueAssociation]int64)
		}
		sh.m[e.key] = e.pos
	}
	s.small = nil
}

func (s *AssociationSet) Get(key UniqueAssociation) (int64, bool) {
	s.changeLock.RLock()
	if s.shards == nil {
		var v int64
		var ok bool
		if s.small != nil {
			for i := range s.small {
				if s.small[i].key == key {
					v, ok = s.small[i].pos, true
					break
				}
			}
		} else if s.hasInline && s.inlineKey == key {
			v, ok = s.inlineVal, true
		}
		s.changeLock.RUnlock()
		return v, ok
	}
	sh := s.shards.shardFor(key)
	s.changeLock.RUnlock()

	sh.mu.RLock()
	defer sh.mu.RUnlock()
	v, ok := sh.m[key]
	return v, ok
}

func (s *AssociationSet) Put(key UniqueAssociation, value int64) {
	// Fast path: an already-sharded set routes straight to its shard under a
	// shared lock, so concurrent writers to distinct shards never serialize.
	s.changeLock.RLock()
	if s.shards != nil {
		sh := s.shards.shardFor(key)
		s.changeLock.RUnlock()
		sh.mu.Lock()
		if sh.m == nil {
			sh.m = make(map[UniqueAssociation]int64)
		}
		sh.m[key] = value
		sh.mu.Unlock()
		return
	}
	s.changeLock.RUnlock()

	s.changeLock.Lock()
	defer s.changeLock.Unlock()

	// Re-check under the write lock: another goroutine may have sharded.
	if s.shards != nil {
		sh := s.shards.shardFor(key)
		sh.mu.Lock()
		if sh.m == nil {
			sh.m = make(map[UniqueAssociation]int64)
		}
		sh.m[key] = value
		sh.mu.Unlock()
		return
	}
	if s.small != nil {
		for i := range s.small {
			if s.small[i].key == key {
				s.small[i].pos = value // in-place update (e.g. disk-mode rewrite)
				return
			}
		}
		s.small = append(s.small, assocEntry{key, value})
		if len(s.small) > shardThreshold {
			s.promote()
		}
		return
	}
	if !s.hasInline {
		s.inlineKey = key
		s.inlineVal = value
		s.hasInline = true
		return
	}
	if s.inlineKey == key {
		s.inlineVal = value // in-place update (e.g. disk-mode position rewrite)
		return
	}
	// Second distinct key: grow the inline entry into a slice.
	s.small = []assocEntry{{s.inlineKey, s.inlineVal}, {key, value}}
	s.hasInline = false
}

func (s *AssociationSet) Delete(key UniqueAssociation) {
	s.changeLock.RLock()
	if s.shards != nil {
		sh := s.shards.shardFor(key)
		s.changeLock.RUnlock()
		sh.mu.Lock()
		delete(sh.m, key)
		sh.mu.Unlock()
		return
	}
	s.changeLock.RUnlock()

	s.changeLock.Lock()
	defer s.changeLock.Unlock()
	if s.shards != nil {
		sh := s.shards.shardFor(key)
		sh.mu.Lock()
		delete(sh.m, key)
		sh.mu.Unlock()
		return
	}
	if s.small != nil {
		for i := range s.small {
			if s.small[i].key == key {
				// Order is irrelevant (queries re-sort), so swap-remove.
				last := len(s.small) - 1
				s.small[i] = s.small[last]
				s.small = s.small[:last]
				return
			}
		}
		return
	}
	if s.hasInline && s.inlineKey == key {
		s.hasInline = false
	}
}

func (s *AssociationSet) Size() int {
	s.changeLock.RLock()
	defer s.changeLock.RUnlock()

	if s.shards == nil {
		if s.small != nil {
			return len(s.small)
		}
		if s.hasInline {
			return 1
		}
		return 0
	}
	n := 0
	for i := range s.shards.shards {
		sh := &s.shards.shards[i]
		sh.mu.RLock()
		n += len(sh.m)
		sh.mu.RUnlock()
	}
	return n
}

// ForEach visits every (key, position). Order is unspecified (the query paths
// re-sort by resolved strings, so tree ordering was never used).
func (s *AssociationSet) ForEach(fn func(key UniqueAssociation, pos int64)) {
	s.changeLock.RLock()
	if s.shards == nil {
		if s.small != nil {
			entries := s.small
			s.changeLock.RUnlock()
			for i := range entries {
				fn(entries[i].key, entries[i].pos)
			}
			return
		}
		inlineKey, inlineVal, has := s.inlineKey, s.inlineVal, s.hasInline
		s.changeLock.RUnlock()
		if has {
			fn(inlineKey, inlineVal)
		}
		return
	}
	shards := s.shards
	s.changeLock.RUnlock()

	for i := range shards.shards {
		sh := &shards.shards[i]
		sh.mu.RLock()
		for k, v := range sh.m {
			fn(k, v)
		}
		sh.mu.RUnlock()
	}
}

// intersect returns a new set of the entries present in both s and other.
func (s *AssociationSet) intersect(other *AssociationSet) *AssociationSet {
	out := newAssociationSet()
	s.ForEach(func(key UniqueAssociation, pos int64) {
		if _, found := other.Get(key); found {
			out.Put(key, pos)
		}
	})
	return out
}

// intersectByAssociate returns a new set of the entries in s whose AssociateTo
// (the associated entry id, e.g. a content hash) also appears in other, ignoring
// the Relation. Plain intersect keys on the full {AssociateTo, Relation} pair, so
// two reverse sets built under different relations — a tag and a media type, say —
// never match even when they point at the same hashes. This variant scopes a
// tag-query result to a media type entirely server-side by comparing hash
// identity alone.
func (s *AssociationSet) intersectByAssociate(other *AssociationSet) *AssociationSet {
	associates := make(map[uint64]struct{}, other.Size())
	other.ForEach(func(key UniqueAssociation, _ int64) {
		associates[key.AssociateTo] = struct{}{}
	})
	out := newAssociationSet()
	s.ForEach(func(key UniqueAssociation, pos int64) {
		if _, found := associates[key.AssociateTo]; found {
			out.Put(key, pos)
		}
	})
	return out
}

// exclude returns a new set of the entries in s that are absent from other.
func (s *AssociationSet) exclude(other *AssociationSet) *AssociationSet {
	out := newAssociationSet()
	s.ForEach(func(key UniqueAssociation, pos int64) {
		if _, found := other.Get(key); !found {
			out.Put(key, pos)
		}
	})
	return out
}
