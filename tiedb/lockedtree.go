package tiedb

import (
	"bytes"
	"sync"

	rbt2 "github.com/emirpasic/gods/v2/trees/redblacktree"
)

// lockedTree is a concurrency-safe, typed red-black tree used for the outer
// index structures (the DB's collection map, per-level entry maps, and the
// outer association maps). It wraps a gods v2 generic tree so keys and values
// are stored unboxed — the boxing these trees used to pay was the largest
// remaining heap cost after the inner AssociationSet was typed.
//
// Unlike AssociationSet these trees are never lazy: they routinely hold many
// entries, so deferring the backing allocation buys nothing.
type lockedTree[K comparable, V any] struct {
	tree *rbt2.Tree[K, V]
	lock sync.RWMutex
}

func newLockedTree[K comparable, V any](comparator func(a, b K) int) *lockedTree[K, V] {
	return &lockedTree[K, V]{tree: rbt2.NewWith[K, V](comparator)}
}

func (t *lockedTree[K, V]) Get(key K) (V, bool) {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.tree.Get(key)
}

func (t *lockedTree[K, V]) Put(key K, value V) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.tree.Put(key, value)
}

func (t *lockedTree[K, V]) Delete(key K) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.tree.Remove(key)
}

func (t *lockedTree[K, V]) Size() int {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.tree.Size()
}

// ForEach visits every (key, value) in order under a read lock.
func (t *lockedTree[K, V]) ForEach(fn func(key K, value V)) {
	t.lock.RLock()
	defer t.lock.RUnlock()
	it := t.tree.Iterator()
	for it.Next() {
		fn(it.Key(), it.Value())
	}
}

// uint64Compare orders entry IDs.
func uint64Compare(a, b uint64) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	default:
		return 0
	}
}

// uniqueValueCompare orders UniqueValue by ParentId then raw value bytes.
func uniqueValueCompare(a, b *UniqueValue) int {
	switch {
	case a.ParentId > b.ParentId:
		return 1
	case a.ParentId < b.ParentId:
		return -1
	default:
		return bytes.Compare(a.Value[:], b.Value[:])
	}
}

// collectionKeyCompare orders collections by Collection+Database name.
func collectionKeyCompare(a, b CollectionKey) int {
	return bytes.Compare(
		[]byte(a.Collection+a.Database),
		[]byte(b.Collection+b.Database),
	)
}
