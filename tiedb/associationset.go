package tiedb

import (
	"sync"

	rbt2 "github.com/emirpasic/gods/v2/trees/redblacktree"
)

// AssociationSet is the inner association subtree: a map from UniqueAssociation
// to an int64 position (a disk offset in disk mode, or an arena index in memory
// mode). It uses gods v2 generics so nodes store the key and value unboxed,
// which is the dominant heap saving on large collections — every reverse
// association lives in one of these nodes.
//
// Like the outer TieTree, it defers allocating its backing tree until it holds a
// second distinct key; a lone entry is kept inline. Forward indexes keyed by
// unique content-address hashes hold exactly one association per key, so this
// spares a whole tree + node in the common case.
//
// It is internal to tiedb: association set algebra reaches callers only through
// Collection.QueryTags, so no gods v2 type parameter ever crosses into api/.
type AssociationSet struct {
	tree       *rbt2.Tree[UniqueAssociation, int64]
	changeLock sync.RWMutex

	inlineKey UniqueAssociation
	inlineVal int64
	hasInline bool
}

// newAssociationSet returns an empty lazy set.
func newAssociationSet() *AssociationSet {
	return &AssociationSet{}
}

// assocCompare orders UniqueAssociation by AssociateTo then Relation. It matches
// UniqueAssociationComparator but is typed for the v2 tree.
func assocCompare(a, b UniqueAssociation) int {
	switch {
	case a.AssociateTo > b.AssociateTo:
		return 1
	case a.AssociateTo < b.AssociateTo:
		return -1
	case a.Relation > b.Relation:
		return 1
	case a.Relation < b.Relation:
		return -1
	default:
		return 0
	}
}

// promote allocates the backing tree and migrates the inline entry into it.
// Caller must hold changeLock.
func (s *AssociationSet) promote() {
	s.tree = rbt2.NewWith[UniqueAssociation, int64](assocCompare)
	if s.hasInline {
		s.tree.Put(s.inlineKey, s.inlineVal)
		s.hasInline = false
	}
}

func (s *AssociationSet) Get(key UniqueAssociation) (int64, bool) {
	s.changeLock.RLock()
	defer s.changeLock.RUnlock()

	if s.tree == nil {
		if s.hasInline && assocCompare(s.inlineKey, key) == 0 {
			return s.inlineVal, true
		}
		return 0, false
	}
	return s.tree.Get(key)
}

func (s *AssociationSet) Put(key UniqueAssociation, value int64) {
	s.changeLock.Lock()
	defer s.changeLock.Unlock()

	if s.tree == nil {
		if !s.hasInline {
			s.inlineKey = key
			s.inlineVal = value
			s.hasInline = true
			return
		}
		if assocCompare(s.inlineKey, key) == 0 {
			s.inlineVal = value // in-place update (e.g. disk-mode position rewrite)
			return
		}
		s.promote() // second distinct key: grow into a real tree
	}
	s.tree.Put(key, value)
}

func (s *AssociationSet) Delete(key UniqueAssociation) {
	s.changeLock.Lock()
	defer s.changeLock.Unlock()

	if s.tree == nil {
		if s.hasInline && assocCompare(s.inlineKey, key) == 0 {
			s.hasInline = false
		}
		return
	}
	s.tree.Remove(key)
}

func (s *AssociationSet) Size() int {
	s.changeLock.RLock()
	defer s.changeLock.RUnlock()

	if s.tree == nil {
		if s.hasInline {
			return 1
		}
		return 0
	}
	return s.tree.Size()
}

// ForEach visits every (key, position) in order under a read lock.
func (s *AssociationSet) ForEach(fn func(key UniqueAssociation, pos int64)) {
	s.changeLock.RLock()
	defer s.changeLock.RUnlock()

	if s.tree == nil {
		if s.hasInline {
			fn(s.inlineKey, s.inlineVal)
		}
		return
	}
	it := s.tree.Iterator()
	for it.Next() {
		fn(it.Key(), it.Value())
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
