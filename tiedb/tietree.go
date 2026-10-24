package tiedb

import (
	"errors"
	"fmt"
	"log/slog"
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

	// pending holds in-flight collection loads (single-flight). colMutex
	// guards it; the load itself runs outside the mutex so a slow load of
	// one collection never blocks requests to the others.
	pending map[CollectionKey]*pendingLoad

	// defaultReverseRelations is applied to every Collection this DB creates
	// that has no entry in reverseOverrides. nil means "index all relations in
	// reverse"; see Collection.reverseRelations.
	defaultReverseRelations []string

	// reverseOverrides pins the reverse-relation set for specific collections,
	// overriding defaultReverseRelations. A collection present here uses its
	// listed relations; absent collections fall back to the default. Set once
	// before collections are created (collections load lazily and apply this at
	// init time), so changes require a triplestore restart.
	reverseOverrides map[CollectionKey][]string

	// blobPolicy is applied to every Collection this DB creates. nil keeps the
	// trie-only behavior; see Collection.blobPolicy and [[BlobPolicy]].
	blobPolicy *BlobPolicy

	// loadMemoryLimit is the soft heap ceiling (GOMEMLIMIT semantics) applied
	// while bulk-loading a collection. 0 leaves the runtime default alone.
	loadMemoryLimit int64

	// locking, when true, makes every loaded collection take an exclusive
	// advisory lock on its .tie file. A server and a direct `tie -f` open (or
	// two servers) then cannot write the same file at once. Opt-in here so the
	// engine's unit tests and embedders that manage their own lifecycle are not
	// surprised; webservice.NewWebservice enables it by default.
	locking bool

	// readOnly opens collections for reading only: no writer goroutine, no
	// advisory lock, and every write returns ErrReadOnly. It is for a reader
	// that must coexist with a live writer (a direct `tie --read-only` beside a
	// running mount).
	readOnly bool
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
		pending:     map[CollectionKey]*pendingLoad{},
	}
}

// pendingLoad is the single-flight record for one collection load: concurrent
// getters for the same key wait on ready instead of each starting their own
// load (which would double-open the file and race two indexes).
type pendingLoad struct {
	ready chan struct{}
	col   *Collection
	err   error
}

// GetCollection returns the collection for key, loading (and in disk mode,
// creating) it on first use. The load runs outside colMutex; only the map
// bookkeeping takes it, so loading a large collection no longer stalls
// requests to every other collection.
//
// A collection that cannot be loaded is NOT fatal: it comes back in sticky
// error state — reads answer empty, every write reports Err() — instead of the
// library calling os.Exit and taking the whole triplestore down with it. Use
// TryGetCollection when you want that error explicitly.
// SetLoadMemoryLimit sets a soft heap ceiling (in bytes, GOMEMLIMIT
// semantics) that applies while a collection is loaded from disk. A bulk load
// with a relaxed GC pacer can otherwise spike the heap on a small machine;
// the limit bounds that peak instead of leaving it to GOGC alone. 0 disables.
func (db *TieTree) SetLoadMemoryLimit(bytes int64) { db.loadMemoryLimit = bytes }

// SetLocking makes newly loaded collections take an exclusive advisory lock on
// their .tie file. Call before a collection is loaded; the setting applies to
// collections loaded afterwards. It exists so a server and a direct
// `tie -f` invocation cannot write the same file concurrently.
func (db *TieTree) SetLocking(on bool) { db.locking = on }

// SetReadOnly opens every collection that this tree loads for reading only.
// Call before a collection is loaded. A read-only collection has no writer
// goroutine and takes no advisory lock, so it can be opened while another
// process holds the file; reads are a load-time snapshot and writes return
// ErrReadOnly.
func (db *TieTree) SetReadOnly(on bool) { db.readOnly = on }

// ErrReadOnly is returned by a write to a collection opened read-only.
var ErrReadOnly = errors.New("collection is open read-only")

func (db *TieTree) GetCollection(key CollectionKey) *Collection {
	col, _ := db.TryGetCollection(key)
	return col
}

// TryGetCollection is GetCollection with the load failure surfaced.
func (db *TieTree) TryGetCollection(key CollectionKey) (*Collection, error) {
	if col, found := db.collections.Get(key); found {
		return col, col.Err()
	}
	return db.loadCollection(key)
}

// LookupCollection returns the collection for key only if it exists — either
// already loaded, or present on disk. It never creates one: a read-role caller
// probing arbitrary collection names must not mint a fresh collection (file,
// directory, writer goroutine and channel buffers) per name.
func (db *TieTree) LookupCollection(key CollectionKey) (*Collection, bool) {
	if col, found := db.collections.Get(key); found {
		return col, true
	}
	if !db.writeToDisk {
		return nil, false
	}
	if _, err := os.Stat(key.Database + "/" + key.Collection + ".tie"); err != nil {
		return nil, false
	}
	col, _ := db.loadCollection(key)
	return col, true
}

func (db *TieTree) loadCollection(key CollectionKey) (*Collection, error) {
	db.colMutex.Lock()
	if col, found := db.collections.Get(key); found {
		db.colMutex.Unlock()
		return col, col.Err()
	}
	if p, ok := db.pending[key]; ok {
		db.colMutex.Unlock()
		<-p.ready
		return p.col, p.err
	}
	p := &pendingLoad{ready: make(chan struct{})}
	db.pending[key] = p
	db.colMutex.Unlock()

	col, err := db.initialize(key.Database, key.Collection, false) // heavy load, no mutex held
	p.col, p.err = col, err

	db.colMutex.Lock()
	db.collections.Put(key, col)
	delete(db.pending, key)
	db.colMutex.Unlock()
	close(p.ready)
	return col, err
}

// Close flushes and closes every live collection's disk writer, blocking until
// each has drained its pending writes, synced, and closed its file handle. It is
// the clean-shutdown counterpart to the periodic sync in dBWriter: call it from
// a signal handler so a terminating server durably persists its tail of writes
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

// DropCollection deletes a collection's entire on-disk state and forgets its
// in-memory index. The live writer (if any) is closed first so its fd is
// released before the .tie file is removed, then the collection is dropped from
// the map; a later GetCollection reloads it lazily from the now-absent file,
// yielding an empty collection. This is the destructive counterpart to the
// additive Add/Restore path — the whole collection is overwritten, not merged.
// A no-op in memory-only mode returns nil after dropping the in-memory entry.
func (db *TieTree) DropCollection(key CollectionKey) error {
	if db.readOnly {
		return ErrReadOnly
	}
	db.colMutex.Lock()
	defer db.colMutex.Unlock()

	if col, found := db.collections.Get(key); found {
		// Remove the map entry FIRST, so a request arriving after this point
		// resolves a fresh collection instead of the dying one. The live writer
		// is then closed (draining its queue and releasing its fd) before the
		// .tie file is removed, so a draining write can never resurrect the file.
		db.collections.Delete(key)
		if db.writeToDisk {
			col.closeDB()
		}
	}

	if !db.writeToDisk {
		return nil
	}

	path := key.Database + "/" + key.Collection + ".tie"
	if db.locking {
		// A collection that was never loaded is not holding the lock; take it
		// for the removal so a direct opener cannot start writing what we are
		// about to delete.
		if _, statErr := os.Stat(path); statErr == nil {
			lk, lerr := lockCollectionFile(path)
			if lerr != nil {
				return lerr
			}
			if lk != nil {
				defer lk.Close()
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// initialize builds one collection index, loading the record file in disk mode.
// A failure is returned (and recorded on the collection as Err) rather than
// exiting: a library must not call os.Exit in the middle of a request, and one
// unreadable collection must not take the other collections down. The returned
// collection is usable in degraded form — reads answer empty, writes report
// the error.
func (db *TieTree) initialize(path string, dbname string, clearExistingDB bool) (*Collection, error) {
	ic := &Collection{}

	ic.writeToDisk = db.writeToDisk
	ic.readOnly = db.readOnly
	ic.dBPath = path
	ic.dBName = dbname
	ic.dBFullPath = path + "/" + dbname + ".tie"
	reverse := db.defaultReverseRelations
	if r, ok := db.reverseOverrides[CollectionKey{Database: path, Collection: dbname}]; ok {
		reverse = r
	}
	ic.SetReverseRelations(reverse)
	ic.SetBlobPolicy(db.blobPolicy)
	ic.loadMemoryLimit = db.loadMemoryLimit

	if db.writeToDisk {
		ic.cache = newTripleCache(defaultTripleCacheSize)
		if db.readOnly {
			// A read-only collection has no writer goroutine, takes no lock and
			// must not truncate, create or otherwise touch the file: it is
			// meant to run beside a live writer (e.g. a mount). Reads are a
			// snapshot of what was on disk when the collection loaded.
			slog.Info("initializing collection read-only", "path", ic.dBFullPath)
			if err := ic.loadDB(ic.dBFullPath); err != nil && !os.IsNotExist(err) {
				err = fmt.Errorf("loading DB %s: %w", ic.dBFullPath, err)
				ic.setIOErr(err)
				slog.Error("loading DB", "path", ic.dBFullPath, "err", err)
				return ic, err
			}
			if err := ic.initReadSize(); err != nil {
				err = fmt.Errorf("sizing DB %s: %w", ic.dBFullPath, err)
				ic.setIOErr(err)
				slog.Error("sizing DB", "path", ic.dBFullPath, "err", err)
				return ic, err
			}
			f, ferr := os.Open(ic.dBFullPath)
			if ferr == nil {
				ic.setFile(f)
			} else if !os.IsNotExist(ferr) {
				err := fmt.Errorf("opening DB %s read-only: %w", ic.dBFullPath, ferr)
				ic.setIOErr(err)
				slog.Error("opening DB read-only", "path", ic.dBFullPath, "err", err)
				return ic, err
			}
			return ic, nil
		}
		slog.Info("initializing collection", "path", ic.dBFullPath)
		if err := os.MkdirAll(path, 0777); err != nil {
			err = fmt.Errorf("creating DB directory %s: %w", path, err)
			ic.setIOErr(err)
			slog.Error("creating DB directory", "path", path, "err", err)
			return ic, err
		}
		if clearExistingDB {
			if err := os.Remove(ic.dBFullPath); err != nil && !os.IsNotExist(err) {
				err = fmt.Errorf("clearing DB %s: %w", ic.dBFullPath, err)
				ic.setIOErr(err)
				slog.Error("clearing DB", "path", ic.dBFullPath, "err", err)
				return ic, err
			}
		}
		if db.locking {
			lk, err := lockCollectionFile(ic.dBFullPath)
			if err != nil {
				err = fmt.Errorf("locking DB %s: %w", ic.dBFullPath, err)
				ic.setIOErr(err)
				slog.Error("locking DB", "path", ic.dBFullPath, "err", err)
				return ic, err
			}
			ic.lock = lk
		}
		if err := ic.loadDB(ic.dBFullPath); err != nil && !os.IsNotExist(err) {
			err = fmt.Errorf("loading DB %s: %w", ic.dBFullPath, err)
			ic.setIOErr(err)
			ic.releaseLock()
			slog.Error("loading DB", "path", ic.dBFullPath, "err", err)
			return ic, err
		}
		ic.dBWriter() // records its own open/sizing failures as Err
	}

	return ic, nil
}
