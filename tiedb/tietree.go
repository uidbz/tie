package tiedb

import (
	"fmt"
	"log"
	"os"
	"sync"

	rbt "github.com/emirpasic/gods/trees/redblacktree"
	"github.com/emirpasic/gods/utils"
)

type TieTree struct {
	*rbt.Tree
	writeToDisk bool
	colMutex    sync.Mutex
	changeLock  sync.RWMutex

	// comparator is retained so a lazy tree can build its rbt.Tree on promotion.
	comparator utils.Comparator

	// A lazy tree defers allocating the embedded rbt.Tree (and its first node)
	// until it holds a second, distinct key. While it holds 0 or 1 entries the
	// Tree is nil and the single entry lives inline. This spares one rbt.Tree +
	// one node per outer key whose sub-tree only ever holds one association — the
	// common case for a forward index keyed by unique content-address hashes.
	inlineKey interface{}
	inlineVal interface{}
	hasInline bool

	// defaultReverseRelations is applied to every Collection this DB creates.
	// nil means "index all relations in reverse"; see Collection.reverseRelations.
	defaultReverseRelations []string
}

// SetDefaultReverseRelations restricts which relations (value1) newly created
// collections index in reverse. Pass nil to index every relation (the default).
// Call before collections are created; existing collections are unaffected.
func (tree *TieTree) SetDefaultReverseRelations(relations []string) {
	tree.defaultReverseRelations = relations
}

func NewDB(writeToDisk bool) *TieTree {
	tree := &TieTree{
		Tree:        rbt.NewWith(KeyComparator),
		writeToDisk: writeToDisk,
	}

	return tree
}

func NewTreeWith(comparator utils.Comparator) *TieTree {
	tree := &TieTree{
		Tree:        rbt.NewWith(comparator),
		comparator:  comparator,
		writeToDisk: false,
	}
	return tree
}

// NewLazyTreeWith returns a tree that defers allocating its rbt.Tree until it
// holds a second distinct key. Until then a single entry is kept inline.
func NewLazyTreeWith(comparator utils.Comparator) *TieTree {
	return &TieTree{
		comparator:  comparator,
		writeToDisk: false,
	}
}

// promote allocates the backing rbt.Tree and migrates the inline entry into it.
// Caller must hold changeLock.
func (tree *TieTree) promote() {
	tree.Tree = rbt.NewWith(tree.comparator)
	if tree.hasInline {
		tree.Tree.Put(tree.inlineKey, tree.inlineVal)
		tree.inlineKey = nil
		tree.inlineVal = nil
		tree.hasInline = false
	}
}

func (tree *TieTree) Get(key interface{}) (value interface{}, found bool) {
	tree.changeLock.RLock()
	defer tree.changeLock.RUnlock()

	if tree.Tree == nil {
		if tree.hasInline && tree.comparator(tree.inlineKey, key) == 0 {
			return tree.inlineVal, true
		}
		return nil, false
	}
	return tree.Tree.Get(key)
}

func (tree *TieTree) Delete(key interface{}) {
	tree.changeLock.Lock()
	defer tree.changeLock.Unlock()

	if tree.Tree == nil {
		if tree.hasInline && tree.comparator(tree.inlineKey, key) == 0 {
			tree.inlineKey = nil
			tree.inlineVal = nil
			tree.hasInline = false
		}
		return
	}
	tree.Tree.Remove(key)
}

func (tree *TieTree) Put(key interface{}, value interface{}) {
	tree.changeLock.Lock()
	defer tree.changeLock.Unlock()

	if tree.Tree == nil {
		if !tree.hasInline {
			tree.inlineKey = key
			tree.inlineVal = value
			tree.hasInline = true
			return
		}
		if tree.comparator(tree.inlineKey, key) == 0 {
			tree.inlineVal = value // in-place update (e.g. disk-mode position rewrite)
			return
		}
		tree.promote() // second distinct key: grow into a real tree
	}
	tree.Tree.Put(key, value)
}

// Size reports the entry count, accounting for the inline (un-promoted) form.
func (tree *TieTree) Size() int {
	tree.changeLock.RLock()
	defer tree.changeLock.RUnlock()

	if tree.Tree == nil {
		if tree.hasInline {
			return 1
		}
		return 0
	}
	return tree.Tree.Size()
}

// ForEach visits every (key, value) in order under a read lock. It is the
// inline-aware replacement for ranging over Iterator(), which is a promoted
// rbt.Tree method that would nil-panic on an un-promoted lazy tree.
func (tree *TieTree) ForEach(fn func(key, value interface{})) {
	tree.changeLock.RLock()
	defer tree.changeLock.RUnlock()

	if tree.Tree == nil {
		if tree.hasInline {
			fn(tree.inlineKey, tree.inlineVal)
		}
		return
	}
	it := tree.Tree.Iterator()
	for it.Next() {
		fn(it.Key(), it.Value())
	}
}

// intersect returns a new tree of the entries present in both t and tree.
// Internal to tiedb: association set algebra is exposed only via QueryTags.
func (t *TieTree) intersect(tree *TieTree) (out *TieTree) {
	out = NewTreeWith(AssociationComparator)
	t.ForEach(func(key, value interface{}) {
		if _, found := tree.Get(key); found {
			out.Put(key, value)
		}
	})
	return out
}

// exclude returns a new tree of the entries in t that are absent from tree.
func (t *TieTree) exclude(tree *TieTree) (out *TieTree) {
	out = NewTreeWith(AssociationComparator)
	t.ForEach(func(key, value interface{}) {
		if _, found := tree.Get(key); !found {
			out.Put(key, value)
		}
	})
	return out
}

func (db *TieTree) GetCollection(key CollectionKey) *Collection {
	db.colMutex.Lock()
	defer db.colMutex.Unlock()

	col, found := db.Get(key)
	if !found {
		col = db.initialize(key.Database, key.Collection, false)
		db.Put(key, col)
	}

	return col.(*Collection)
}

func (db *TieTree) initialize(path string, dbname string, clearExistingDB bool) *Collection {
	ic := Collection{}

	ic.writeToDisk = db.writeToDisk
	ic.dBPath = path
	ic.dBName = dbname
	ic.dBFullPath = path + "/" + dbname + ".tie"
	ic.SetReverseRelations(db.defaultReverseRelations)

	if db.writeToDisk {
		fmt.Println("Initializing", ic.dBFullPath)
		if err := os.MkdirAll(path, 0777); err != nil {
			log.Fatal("Error creating DB directory ", path, ": ", err, "\nExiting")
		}
		ic.freespace = make(chan int64, MaxFreespace)
		ic.cache = newTripleCache(defaultTripleCacheSize)
		if clearExistingDB {
			if err := os.Remove(ic.dBFullPath); err != nil && !os.IsNotExist(err) {
				log.Fatal("Error clearing DB ", ic.dBFullPath, ": ", err, "\nExiting")
			}
		}
		err := ic.loadDB(ic.dBFullPath)
		if err != nil && !os.IsNotExist(err) {
			log.Fatal("Error loading DB:", err, "\nExiting")
		}
		ic.dBWriter()
	}

	return &ic
}
