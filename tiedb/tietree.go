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
	ic.values = NewTreeWith(ValueComparator)

	if db.writeToDisk {
		fmt.Println("Initializing", ic.dBFullPath)
		os.MkdirAll(path, 0777)
		ic.freespace = make(chan FileEntry, MaxFreespace)
		if clearExistingDB {
			os.Remove(ic.dBFullPath)
		}
		err := ic.loadDB(ic.dBFullPath)
		if err != nil && !os.IsNotExist(err) {
			log.Fatal("Error loading DB:", err, "\nExiting")
		}
		ic.dBWriter()
	}
	e := ic.insert(dbname)

	ic.level = e.Level
	ic.id = e.Id

	return &ic
}
