// Package tiedb is the triple store: an append-only record file plus an
// in-memory index (a value trie plus forward and reverse association trees)
// that a background writer goroutine keeps on disk.
//
// # File layout
//
// A collection file is a sequence of fixed-size ENTRY_SIZE records: value
// entries (TYPE_ENTRY), associations (TYPE_ASSOCIATION) and hash entries
// (TYPE_HASH). Records are never rewritten in place; a delete writes a
// TYPE_DELETE tombstone and the slot goes on a freespace stack for reuse.
// The on-disk format is stable — changing it needs a migration and a
// CHANGELOG entry.
//
// # Lock layers (outermost to innermost)
//
//  1. changeMutex — one write lock per collection. It serialises mutations
//     (addLocked, deleteLocked, insertLocked, CheckIndex) so a composite
//     operation, e.g. Update's delete+add, is one critical section. Callers
//     that need several mutations to land together take it once and call the
//     *Locked helpers; never take it twice (it is not reentrant).
//  2. lockedTree shard RWMutex — the trie, association and hash maps are
//     sharded hash maps. Readers take a shard read lock for the lookup only.
//  3. AssociationSet changeLock — guards one subject's (or one value's)
//     association set: an inline entry, a small slice, or shards.
//
// A lock at layer n may be taken while holding layer n-1, never the reverse.
// The writer goroutine takes none of them: it only consumes FileMod values
// from dBWriteQueue, so holding changeMutex while waiting on a dBReadQueue
// reply cannot deadlock.
//
// # Iteration rules
//
// Callback-based iteration (lockedTree.ForEach, AssociationSet.ForEach,
// Collection.ForEachTriple) must not hold a lock across the callback: the
// callback resolves values, which re-enters the store (levelOf, resolveEntry)
// and deadlocks or serialises everything. Each instead snapshots under the
// lock — lockedTree.ForEach copies each shard, AssociationSet.ForEach copies
// its small tier — and then calls out lock-free. Callbacks must therefore
// tolerate a snapshot: entries added after the copy are not visited, and a
// concurrent delete may already have removed one that is.
//
// # Read-your-writes
//
// Add reserves a slot, queues the FILE_ADD, and only then inserts into the
// index, so any reader that can see an index entry finds the write already
// queued; the writer drains the queue before serving a read (readSlot).
//
// # Search
//
// Collection.Search is a derived read view, not another stored structure. It
// builds a transient corpus from the forward-association index and the value
// trie (levelOf + getValueString), narrowing candidates through the reverse
// tie-type index when the caller names tie-types. The build performs no
// record-file I/O. The corpus is cached per (relations, tie-types) and
// invalidated by searchGen, which every forward-index mutation bumps; memory is
// bounded by SetSearchCacheBytes. See ../docs/search.md.
package tiedb
