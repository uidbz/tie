package tiedb

import (
	"fmt"
	"log"
	"os"
	"sync"
)

// TieTree is the top-level database handle: a typed, concurrency-safe map from
// CollectionKey to *Collection, plus the settings applied to collections it
// creates. The per-level entry and association indexes are separate lockedTree
// instances held on each Collection.
type TieTree struct {
	collections *lockedTree[CollectionKey, *Collection]
	writeToDisk bool
	colMutex    sync.Mutex

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
	return &TieTree{
		collections: newLockedTree[CollectionKey, *Collection](collectionKeyCompare),
		writeToDisk: writeToDisk,
	}
}

func (db *TieTree) GetCollection(key CollectionKey) *Collection {
	db.colMutex.Lock()
	defer db.colMutex.Unlock()

	col, found := db.collections.Get(key)
	if !found {
		col = db.initialize(key.Database, key.Collection, false)
		db.collections.Put(key, col)
	}

	return col
}

func (db *TieTree) initialize(path string, dbname string, clearExistingDB bool) *Collection {
	ic := Collection{}

	ic.writeToDisk = db.writeToDisk
	ic.dBPath = path
	ic.dBName = dbname
	ic.dBFullPath = path + "/" + dbname + ".tie"
	ic.SetReverseRelations(db.defaultReverseRelations)

	if db.writeToDisk {
		fmt.Fprintln(os.Stderr, "Initializing", ic.dBFullPath)
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
