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
		writeToDisk: false,
	}
	return tree
}

func (tree *TieTree) Get(key interface{}) (value interface{}, found bool) {
	tree.changeLock.RLock()
	defer tree.changeLock.RUnlock()

	return tree.Tree.Get(key)
}

func (tree *TieTree) Delete(key interface{}) {
	tree.changeLock.Lock()
	defer tree.changeLock.Unlock()

	tree.Tree.Remove(key)
}

func (tree *TieTree) Put(key interface{}, value interface{}) {
	tree.changeLock.Lock()
	defer tree.changeLock.Unlock()

	tree.Tree.Put(key, value)
}

func (t *TieTree) Intersect(tree *TieTree) (out *TieTree) {
	out = &TieTree{
		Tree:        rbt.NewWith(AssociationComparator),
		writeToDisk: false,
	}

	c := make(chan *rbt.Node, 10000)

	go func() {
		it := t.Iterator()
		for it.Next() {
			if _, found := tree.Get(it.Key()); found {
				c <- it.Node()
			}
		}
		close(c)
	}()
	for x := range c {
		out.Put(x.Key, x.Value)
	}

	return out
}

func (t *TieTree) Exclude(tree *TieTree) (out *TieTree) {
	out = &TieTree{
		Tree:        rbt.NewWith(AssociationComparator),
		writeToDisk: false,
	}

	c := make(chan *rbt.Node, 10000)

	go func() {
		it := t.Iterator()
		for it.Next() {
			if _, found := tree.Get(it.Key()); !found {
				c <- it.Node()
			}
		}
		close(c)
	}()
	for x := range c {
		out.Put(x.Key, x.Value)
	}

	return out
}

func (t *TieTree) Combine(tree *TieTree) (out *TieTree) {
	out = t

	c := make(chan *rbt.Node, 10000)

	go func() {
		it := tree.Iterator()
		for it.Next() {
			c <- it.Node()
		}
		close(c)
	}()
	for x := range c {
		out.Put(x.Key, x.Value)
	}

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
		os.MkdirAll(path, 0777)
		ic.freespace = make(chan int64, MaxFreespace)
		if clearExistingDB {
			os.Remove(ic.dBFullPath)
		}
		err := ic.loadDB(ic.dBFullPath)
		if err != nil && !os.IsNotExist(err) {
			log.Fatal("Error loading DB:", err, "\nExiting")
		}
		ic.dBWriter()
	}
	// e, dbLevel := ic.insert(dbname)

	// ic.level = dbLevel
	// ic.id = e.Id

	return &ic
}
