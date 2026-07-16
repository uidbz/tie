package tiedb

import (
	"sync"
)

// lockedTreeShards is the number of independently-locked map shards each outer
// index is split across. A power of two so the shard index is a cheap mask.
// Sharding lets the concurrent load workers write distinct keys without
// serializing on a single global lock — the outer trees' one write lock was the
// load-speed floor once the inner AssociationSet stopped being the bottleneck.
const lockedTreeShards = 16

// lockedTree is a concurrency-safe, typed map used for the outer index
// structures (the DB's collection map, per-level entry maps, and the outer
// association maps). It is split into lockedTreeShards independently-locked
// map shards, so concurrent writers to different shards never serialize.
//
// It replaces the former single-RWMutex red-black tree. The query paths never
// rely on outer-key ordering (entry/uniqueValue/collection lookups are point
// Gets; the only ForEach — full-collection export in ForEachTriple — re-sorts
// each subtree by resolved strings), so dropping order is free. A map also
// collapses the tens of millions of individually-allocated rbt nodes into a
// handful of backing arrays, cutting the live-object count the GC must scan
// during a bulk load.
//
// Unlike AssociationSet these trees are never lazy and never tiered: they
// routinely hold many entries, and there are only a handful of instances
// (~4 per trie level, of which there are few), so the fixed per-instance cost
// of the shard array is negligible.
type lockedTree[K comparable, V any] struct {
	shards [lockedTreeShards]lockedTreeShard[K, V]
	hash   func(K) uint64
}

type lockedTreeShard[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V // lazily allocated on first write
}

func newLockedTree[K comparable, V any](hash func(K) uint64) *lockedTree[K, V] {
	return &lockedTree[K, V]{hash: hash}
}

func (t *lockedTree[K, V]) shardFor(key K) *lockedTreeShard[K, V] {
	return &t.shards[t.hash(key)&(lockedTreeShards-1)]
}

func (t *lockedTree[K, V]) Get(key K) (V, bool) {
	sh := t.shardFor(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	v, ok := sh.m[key]
	return v, ok
}

func (t *lockedTree[K, V]) Put(key K, value V) {
	sh := t.shardFor(key)
	sh.mu.Lock()
	if sh.m == nil {
		sh.m = make(map[K]V)
	}
	sh.m[key] = value
	sh.mu.Unlock()
}

func (t *lockedTree[K, V]) Delete(key K) {
	sh := t.shardFor(key)
	sh.mu.Lock()
	delete(sh.m, key)
	sh.mu.Unlock()
}

func (t *lockedTree[K, V]) Size() int {
	n := 0
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.RLock()
		n += len(sh.m)
		sh.mu.RUnlock()
	}
	return n
}

// ForEach visits every (key, value). Order is unspecified: the only caller
// (full-collection export) re-sorts by resolved strings, so map order is fine.
func (t *lockedTree[K, V]) ForEach(fn func(key K, value V)) {
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.RLock()
		for k, v := range sh.m {
			fn(k, v)
		}
		sh.mu.RUnlock()
	}
}

// The hashers below spread keys across shards. They only need to scatter the
// low log2(lockedTreeShards) bits well; a full-quality hash is unnecessary.

const hashMix = 0x9e3779b97f4a7c15 // fractional bits of the golden ratio

// uint64Hash scatters entry IDs (which are otherwise sequential).
func uint64Hash(k uint64) uint64 {
	return k * hashMix
}

// hashHash mixes the first bytes of a whole-value hash key. Hash keys are
// already high-entropy, so folding the leading 16 bytes suffices.
func hashHash(k [32]byte) uint64 {
	lo := uint64(k[0]) | uint64(k[1])<<8 | uint64(k[2])<<16 | uint64(k[3])<<24 |
		uint64(k[4])<<32 | uint64(k[5])<<40 | uint64(k[6])<<48 | uint64(k[7])<<56
	hi := uint64(k[8]) | uint64(k[9])<<8 | uint64(k[10])<<16 | uint64(k[11])<<24 |
		uint64(k[12])<<32 | uint64(k[13])<<40 | uint64(k[14])<<48 | uint64(k[15])<<56
	return lo ^ (hi * hashMix)
}

// uniqueValueHash mixes ParentId with an FNV-1a hash of the value bytes.
func uniqueValueHash(k UniqueValue) uint64 {
	h := uint64(14695981039346656037)
	for _, b := range k.Value {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h ^ (k.ParentId * hashMix)
}

// collectionKeyHash is an FNV-1a hash over the two name strings.
func collectionKeyHash(k CollectionKey) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(k.Collection); i++ {
		h ^= uint64(k.Collection[i])
		h *= 1099511628211
	}
	for i := 0; i < len(k.Database); i++ {
		h ^= uint64(k.Database[i])
		h *= 1099511628211
	}
	return h
}
