# tiedb internals: data structures, file format, memory

This documents how `tiedb` stores triples in memory and on disk, and the memory
trade-offs that shaped the current design. It describes the code as it stands in
`tiedb/` — treat it as a map for reading that package, not a spec to code against.

## The triple model

Everything in a collection is a triple: `(key, value1, value2)`. In the tagging
layer these read as `(subject, relation, object)`, e.g.

```
hash   tag        sometag
hash   tag        sometag2
hash   filename   "myfile.ext"
```

A query fetches all triples for a key (`Get`), and — when asked — also the
triples where the key appears as `value2` (the reverse direction). Reverse
lookup is what turns `sometag` back into the set of hashes tagged with it.

## Strings become IDs: the trie of unique values

Strings are never stored inline in the index. Each string is chopped into
`SIZE_VALUE`-byte (24 B) chunks and inserted into a per-level trie of unique
values. A string of length N occupies `ceil(N/24)` levels; the last chunk's
entry ID is the string's identity everywhere else.

Two red-black trees per level implement the trie (`entryLevel` in
`datastructures.go`). All index trees are `lockedTree[K, V]` — a typed
`github.com/emirpasic/gods/v2` red-black tree behind an `RWMutex`, so keys and
values are stored unboxed (`lockedtree.go`):

- `entries`: `entryID (uint64)` → `*UniqueValue{ParentId, [24]byte chunk}`
- `uniqueValues`: `*UniqueValue{ParentId, chunk}` → `entryID` (the reverse map,
  used to dedup and to resolve an existing string to its ID)

`insert` (`collection.go`) walks the chunks, reusing existing IDs where the
prefix already exists and minting new ones (`nextID`) otherwise. `getValueString`
walks back up `ParentId` links to reassemble the full string from an ID+level.

Consequence: a value shared by many triples (a media type, a tag name) is stored
once. Long, unique values (paths, filenames) cost one entry per 24-byte chunk.

## Associations: forward and reverse indexes

A triple's association is stored keyed by entry IDs, not strings. Per level
(`entryLevel`):

- `associations`: `key (uint64)` → `*AssociationSet` keyed by
  `UniqueAssociation{AssociateTo: value2, Relation: value1}` → `int64` position
- `reverseAssociations`: `value2 (uint64)` → `*AssociationSet` keyed by
  `UniqueAssociation{AssociateTo: key, Relation: value1}` → `int64` position

The subtree (`AssociationSet` in `associationset.go`) is a typed gods v2 tree
`Tree[UniqueAssociation, int64]`, so its key and value are stored unboxed. The
`int64` value is a *position* in both modes:

- **disk-backed mode**: the byte offset of that triple's record in the on-disk
  file (`-1` until the writer assigns it). The full `(key, value1, value2)` IDs
  are read back from disk on query, cached in a bounded LRU (`triplecache.go`) so
  hot triples avoid a re-read.
- **memory-only mode**: an index into the collection's in-memory `arena
  []Triple` (`collection.go`). Queries never touch disk; there is no cache or
  file. Freed slots are recycled via `arenaFree`, mirroring the disk `freespace`
  list.

Keeping the value a uniform `int64` in both modes is deliberate: it lets the
subtree stay a single `Tree[UniqueAssociation, int64]` instead of a union type
sized to the larger `Triple`, which measured worse.

Both indexes hold the same position; the reverse index is a pure in-memory
acceleration structure, never persisted separately — it is rebuilt from the file
on load. `resolveTriple` turns a position back into a `Triple` uniformly (arena
lookup in memory mode, cache/disk read in disk mode) for the query path.

`AssociationSet` is also *lazy*: a subtree holding a single association keeps it
inline in the struct and never allocates a backing tree — the common case for a
forward index keyed by unique content-address hashes. It promotes to a real tree
only on the second distinct key. Set algebra (`intersect`/`exclude`, used by
`QueryTags`) stays internal to `tiedb`; no gods type parameter crosses into the
`api` package.

`insertAssociation` (`collection.go`) builds the forward node always, and the
reverse node only when `shouldBuildReverse` allows it (see below).

## On-disk file format

The `.tie` file is a flat, append-oriented log of fixed-size records. Every
record is `ENTRY_SIZE` = 50 bytes, all integers little-endian:

```
ENTRY_SIZE = SIZE_DATATYPE(2) + SIZE_LEVEL(8) + SIZE_ID(8)
           + SIZE_PARENTID(8)  + SIZE_VALUE(24)          = 50
```

The first 2 bytes are the record type. `SIZE_VALUE` is 24 specifically so an
entry record (`2 + 8 + 8 + 8 + 24`) fills the same 50-byte frame as an
association record (`2 + 6×8`).

Record types (`datastructures.go`):

- `TYPE_ENTRY` (11) — one trie chunk. Layout: `datatype | level | entryID |
  parentID | value[24]`. Written by `EntryToBytes`, parsed by `bufToEntry`.
- `TYPE_ASSOCIATION` (12) — one triple. Layout: `datatype | entry_level |
  entry_id(key) | value2_level | value2 | value1_level | value1`. Written by
  `Triple.toBytes`, parsed by `bufToAssociation`. Note value2 precedes value1 on
  disk.
- `TYPE_DELETE` (10) — a tombstone. `deleteAssociation` overwrites a record's
  first 2 bytes with `TYPE_DELETE`; the slot's offset is pushed onto the
  `freespace` channel for reuse by the next add.

Writes go through a single serialized writer goroutine (`dBWriter` in
`filehandling.go`) over `dBWriteQueue`. It reuses a freed slot when one is
available, otherwise appends at `db_size`. The file is held open for a 10-second
idle window (`closeAfter`) before being synced and closed — so a `closeDB` right
after writes can take up to that long to settle. `db_size` must stay a multiple
of `ENTRY_SIZE`; a mismatch on open is treated as corruption.

### Loading is two-pass

`loadDB` reads the whole file twice via `loadPass`:

1. `loadEntries` inserts every `TYPE_ENTRY` (and reclaims `TYPE_DELETE` slots
   into `freespace`), skipping associations.
2. `loadAssociations` inserts every `TYPE_ASSOCIATION`.

The two passes are required, not incidental: `insertAssociation` →
`shouldBuildReverse` resolves a triple's relation (`value1`) back to a string via
the entries trie to decide whether to build a reverse node. The loader runs
worker goroutines that consume records in arbitrary order, so an association can
be seen before the entry it references. Inserting all entries first guarantees
every relation string resolves during pass 2.

## Memory considerations

The dominant in-memory costs are (1) the trie entries and their two rbt nodes
per chunk, and (2) the association indexes. For metadata-heavy stores the
association indexes dominate, and historically each triple paid for *two* rbt
nodes — one forward, one reverse — even when nothing ever queried that relation
in reverse.

Three levers cut this ~45% at 1M triples (862 → 473 MiB, disk/reverse=all):
lazy single-entry association subtrees, opt-in reverse indexing (below), and
moving every tree from `interface{}`-boxed gods v1 to typed gods v2 generics
(`lockedTree` for the outer trees, `AssociationSet` for the subtrees). After the
v2 migration the remaining heap is genuine node structs and payload, not boxing.

### Two modes: memory-only vs disk-backed

`NewDB(writeToDisk)` selects the mode for every collection it creates:

- **disk-backed** (`writeToDisk=true`): the `int64` position is a byte offset;
  the `Triple` is read from disk on query and served from a bounded LRU cache
  thereafter. Lowest resident memory. Adds are durable and `Sync()` waits for the
  writer to flush them.
- **memory-only** (`writeToDisk=false`): the `int64` position indexes the
  in-memory `arena []Triple`; queries never touch disk and there is no cache or
  file. `Add` inserts directly and `Sync()` is a no-op. Fastest, higher memory,
  non-durable.

The two paths meet at `resolveTriple`, so `Get`/`Sort` are mode-agnostic. A
query walks the subtree in memory in both modes — there is no per-query
serialization; concurrent reads each use their own reply channel when a disk
read is needed.

### Opt-in reverse associations

Most relations are never queried in reverse. In this codebase reverse lookup is
used only for `tag` (files with a tag), `path` (path → UID), and `parent` (a
directory's children). Relations like `filename`, `media-type`, and `size` got a
reverse node that was pure overhead.

A collection now carries `reverseRelations map[string]bool`:

- `nil` → index every relation in reverse (the original behavior; kept as the
  default so generic `tiedb` users are unaffected).
- non-nil → `insertAssociation` builds a reverse node only for relations in the
  set; `shouldBuildReverse` gates it.

Wiring: `TieTree.SetDefaultReverseRelations` sets a DB-wide default that
`initialize` copies into each new `Collection` (via `Collection.SetReverseRelations`);
`WebserviceConfig.ReverseRelations` plumbs it through `NewWebservice`. The
`tie-daemon` sets `{"tag", "path", "parent"}`.

Effect: on a store with K metadata relations per item where only one is queried
in reverse, reverse-index nodes drop toward 1/K of the full set — e.g. 4
relations per file with a `tag`-only allowlist cut reverse nodes to ~25% of the
full index in a synthetic test.

Trade-off: on `tie-daemon`, generic reverse queries (`tie get -r <x>`, and the
include/exclude tag algebra in `QueryTags`) resolve only for whitelisted
relations. Collections that never set an allowlist keep full reverse generality.

### Notes for future work

- The reverse index is derived state — it can always be rebuilt from the file,
  which is what makes the two-pass load and the opt-in filter safe.
- Boxing (done): rbt keys/values used to be `interface{}` and heap-boxed. All
  trees now use gods v2 generics (`lockedTree[K,V]`, `AssociationSet`), so keys
  and the `int64` positions are stored unboxed. pprof after the migration shows
  no remaining boxing; the heap is node structs plus payload. Forking gods for a
  tighter node layout (dropping some of the child/parent/color pointer overhead)
  was considered and declined — the remaining per-node cost is small next to what
  boxing removal already banked.
- Record layout: highwayhash keys are stored as 64-char hex → 3 trie chunks per
  hash; raw 32-byte hashes would be 2. Changing `SIZE_VALUE` or storing raw
  bytes is a breaking `.tie` format change (migrate via `tie dump` → `tie
  restore`), deliberately deferred.
- A lazy reverse index (build a value's reverse subtree on first reverse query
  and cache it) was considered as an alternative that keeps full generality at
  the cost of an O(n) first query; the opt-in approach was chosen because this
  codebase's reverse queries hit a small, known set of relations.
