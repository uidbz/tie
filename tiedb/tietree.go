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

	// defaultReverseRelations is applied to every Collection this DB creates
	// that has no entry in reverseOverrides. nil means "index all relations in
	// reverse"; see Collection.reverseRelations.
	defaultReverseRelations []string

	// reverseOverrides pins the reverse-relation set for specific collections,
	// overriding defaultReverseRelations. A collection present here uses its
	// listed relations; absent collections fall back to the default. Set once
	// before collections are created (collections load lazily and apply this at
	// init time), so changes require a daemon restart.
	reverseOverrides map[CollectionKey][]string

	// blobPolicy is applied to every Collection this DB creates. nil keeps the
	// trie-only behavior; see Collection.blobPolicy and [[BlobPolicy]].
	blobPolicy *BlobPolicy
}

// SetBlobPolicy sets a whole-value blob policy applied to newly created
// collections: matching values (e.g. content-address hashes) are stored as a
// single hash entry instead of being chunked into the trie. Pass nil for the
// default trie-only behavior. Call before collections are created; existing
// collections are unaffected.
func (tree *TieTree) SetBlobPolicy(p *BlobPolicy) {
	tree.blobPolicy = p
}

// SetDefaultReverseRelations restricts which relations (value1) newly created
// collections index in reverse. Pass nil to index every relation (the default).
// Call before collections are created; existing collections are unaffected.
func (tree *TieTree) SetDefaultReverseRelations(relations []string) {
	tree.defaultReverseRelations = relations
}

// SetReverseRelationsOverrides pins the reverse-relation set for specific
// collections, overriding the default set by SetDefaultReverseRelations. Each
// keyed collection indexes exactly its listed relations in reverse; collections
// with no entry use the default. Call before collections are created; existing
// collections are unaffected. The reverse index is rebuilt from forward triples
// at load time, so a changed override takes effect after one restart.
func (tree *TieTree) SetReverseRelationsOverrides(overrides map[CollectionKey][]string) {
	tree.reverseOverrides = overrides
}

func NewDB(writeToDisk bool) *TieTree {
	return &TieTree{
		collections: newLockedTree[CollectionKey, *Collection](collectionKeyHash),
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

// Close flushes and closes every live collection's disk writer, blocking until
// each has drained its pending writes, synced, and closed its file handle. It is
// the clean-shutdown counterpart to the periodic sync in dBWriter: call it from
// a signal handler so a terminating daemon durably persists its tail of writes
// instead of relying on the kernel to flush the page cache. A no-op in
// memory-only mode (collections have no writer goroutine then).
func (db *TieTree) Close() {
	if !db.writeToDisk {
		return
	}
	db.colMutex.Lock()
	defer db.colMutex.Unlock()
	db.collections.ForEach(func(_ CollectionKey, col *Collection) {
		col.closeDB()
	})
}

func (db *TieTree) initialize(path string, dbname string, clearExistingDB bool) *Collection {
	ic := Collection{}

	ic.writeToDisk = db.writeToDisk
	ic.dBPath = path
	ic.dBName = dbname
	ic.dBFullPath = path + "/" + dbname + ".tie"
	reverse := db.defaultReverseRelations
	if r, ok := db.reverseOverrides[CollectionKey{Database: path, Collection: dbname}]; ok {
		reverse = r
	}
	ic.SetReverseRelations(reverse)
	ic.SetBlobPolicy(db.blobPolicy)

	if db.writeToDisk {
		fmt.Fprintln(os.Stderr, "Initializing", ic.dBFullPath)
		if err := os.MkdirAll(path, 0777); err != nil {
			log.Fatal("Error creating DB directory ", path, ": ", err, "\nExiting")
		}
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
