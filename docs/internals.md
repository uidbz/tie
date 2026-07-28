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

### "Triple" vs. "association" — two terms, two meanings

The code uses both words deliberately; they are not synonyms:

- A **triple** is the stored data record — the `Triple` struct
  `(key, value1, value2)`. It is what gets serialized (`toBytes`), held in the
  arena/cache, and returned from queries (`TripleSet`, `StringTriple`).
- An **association** is an *index* over triples — the structures that link entry
  IDs so a triple can be found from one of its members. `associations` /
  `reverseAssociations`, `AssociationSet`, and `insertAssociation` are all index
  concepts. The subtree key `UniqueAssociation{AssociateTo, Relation}` is a
  triple with the pivot entry factored out (the other two IDs).

Rule of thumb: if it holds or moves the full record, it's a *triple*; if it
links or looks records up, it's an *association*. So `FileMod.Triple` carries a
`*Triple` to the writer, while `insertAssociation` inserts that triple into the
association index.

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

### Whole-value hash entries (blob policy)

High-entropy values are the trie's worst case: a 64-char hex content hash shares
no prefixes with any other, so it mints 3 fresh chunk entries (6 rbt nodes) and
the trie's dedup buys nothing. A `Collection` may carry a `BlobPolicy`
(`datastructures.go`) that diverts such values out of the chunk trie into a
whole-value hash store — one `id ↔ [32]byte` entry instead of three chunks.

- `BlobPolicy.Encode(value) (raw []byte, ok bool)` decides, deterministically on
  the value alone, whether a value is an opaque fixed-size blob and returns its
  raw bytes; `Decode` is the inverse. Determinism matters: the same hash is the
  `Key` of many triples, so inconsistent routing would mint two IDs for one value.
- Only encodings of exactly 32 bytes take the hash path; anything else falls back
  to the trie, so the fixed 50-byte frame is never violated.
- `tiedb` stays domain-agnostic — it only knows "some values are 32-byte blobs."
  `metadata.HexHashBlobPolicy` supplies the content-hash implementation (reusing
  `IsHexHash` + `hex`), wired via `TieTree.SetBlobPolicy` at DB construction.
  `nil` (the default) keeps the trie-only behavior, so existing collections and
  generic `tiedb` users are unaffected.

Hash entries carry the `HASH_LEVEL` sentinel instead of a real trie level, so
their associations live in dedicated `hashAssociations`/`hashReverseAssociations`
trees rather than a per-level `entryLevel`. Entry IDs are collection-global, so
keying those trees by ID never collides. `getValueString` reconstructs the string
via `BlobPolicy.Decode` when the level is `HASH_LEVEL`.

## Associations: forward and reverse indexes

A triple's association is stored keyed by entry IDs, not strings. Per level
(`entryLevel`):

- `associations`: `key (uint64)` → `*AssociationSet` keyed by
  `UniqueAssociation{AssociateTo: value2, Relation: value1}` → `int64` position
- `reverseAssociations`: `value2 (uint64)` → `*AssociationSet` keyed by
  `UniqueAssociation{AssociateTo: key, Relation: value1}` → `int64` position

The subtree (`AssociationSet` in `associationset.go`) maps
`UniqueAssociation → int64`. Query paths never rely on key ordering
(`Sort` re-sorts by resolved strings; `intersect`/`exclude` use point lookups),
so it is not an ordered tree — it picks the most compact of three
representations by size (see "Tiered representation" below). The `int64` value
is a *position* in both modes:

- **disk-backed mode**: the byte offset of that triple's record in the on-disk
  file (`-1` until the writer assigns it). The full `(key, value1, value2)` IDs
  are read back from disk on query, cached in a bounded LRU (`triplecache.go`) so
  hot triples avoid a re-read.
- **memory-only mode**: an index into the collection's in-memory `arena
  []Triple` (`collection.go`). Queries never touch disk; there is no cache or
  file. Freed slots are recycled via `arenaFree`, mirroring the disk `freespace`
  list.

Keeping the value a uniform `int64` in both modes is deliberate: it keeps every
entry a fixed 16 bytes (`UniqueAssociation` + position) instead of a union type
sized to the larger `Triple`, which measured worse.

Both indexes hold the same position; the reverse index is a pure in-memory
acceleration structure, never persisted separately — it is rebuilt from the file
on load. `resolveTriple` turns a position back into a `Triple` uniformly (arena
lookup in memory mode, cache/disk read in disk mode) for the query path.

### Tiered representation

`AssociationSet` grows through three representations, chosen by size, because
the workload is bimodal: a forward index keyed by unique content-address hashes
holds ~1 association per key, while a reverse index keyed by a shared value2
(e.g. a "file" relation) can hold millions under one key.

1. **inline** — a single association is held in the struct's `inlineKey` /
   `inlineVal` fields; nothing is allocated. This is the common forward-index
   case.
2. **slice** — on the second distinct key it grows into a flat `[]assocEntry`
   (a `{key, pos}` pair per entry). For the small-to-medium sets that make up
   the bimodal middle this is the most memory-compact form: a Go map carries
   bucket and control-word overhead even when nearly empty (~272 B for a
   2-entry map vs ~88 B for a 2-entry slice), and a linear scan over a handful
   of entries is as fast as hashing and stays in cache. Because no query relies
   on ordering, `Delete` is a swap-remove.
3. **sharded map** — once a set exceeds `shardThreshold` (256) it splits into
   `assocShards` (16) independently-locked `map[UniqueAssociation]int64` shards.
   This is reached only by the reverse index's shared-value hot sets, where two
   things matter: the fixed per-shard overhead (~1 KiB of mutexes + map headers)
   finally amortizes to a few percent, and — crucially — many concurrent load
   workers inserting into the same large set no longer serialize on one lock,
   since a key's shard is chosen by hashing it.

The tiering is what makes the small-set case cheap: an earlier design allocated
the 16-shard array on the *second* key, so every 2-entry set paid the full
~1 KiB shard overhead — a large regression on a bimodal collection. Set algebra
(`intersect`/`exclude`, used by `QueryTags`) stays internal to `tiedb`.

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
- `TYPE_HASH` (13) — one whole-value hash entry (see the blob policy above).
  Layout: `datatype | level | entryID | raw hash[32]`. The 32 raw bytes occupy
  the frame's contiguous `parentID(8) + value(24)` region, so `ENTRY_SIZE` is
  unchanged; the level field is always `HASH_LEVEL`. Written by `HashToBytes`,
  parsed by `bufToHash`, loaded in pass 1 (it is an entry, not an association).
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

Each pass reads through one reusable buffer (reallocating it per read churned
gigabytes of garbage on a large file) and feeds a modestly-sized channel to the
workers; the reader `copy`s each record into a fixed-size array before sending,
so buffer reuse is safe. `loadPass` fans out to several workers, and the GC
pacer is relaxed (`debug.SetGCPercent`) for the load's duration — a bulk load
builds an almost-entirely-retained heap, so the default pace wastes cycles
re-scanning a live set that never shrinks.

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

The full optimization history — every lever, the real-`db1.tie` load/heap
measurements, and two by-value inlining experiments that were built, measured,
and reverted (both lost to Go map bucket slack) — is written up in
[`memory-optimization.md`](memory-optimization.md). Read it before attempting
another association-index memory change; the GiB-scale structural levers are
exhausted at the current baseline.

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
- Record layout: highwayhash keys used to be stored as 64-char hex → 3 trie
  chunks per hash. The `TYPE_HASH` record + blob policy (above) now stores them
  as one raw 32-byte entry, reusing the existing 50-byte frame (no `SIZE_VALUE`
  change). This is opt-in via `SetBlobPolicy`; a store written without the policy
  keeps hashes as trie chunks, and the two forms can coexist in one file since
  the record type gates interpretation. Migrate an old store to the compact form
  via `tie dump` → restore into a policy-enabled DB.
- A lazy reverse index (build a value's reverse subtree on first reverse query
  and cache it) was considered as an alternative that keeps full generality at
  the cost of an O(n) first query; the opt-in approach was chosen because this
  codebase's reverse queries hit a small, known set of relations.

## Why triples, and why not just SQL

Triple stores are not novel — RDF/SPARQL, Datomic and friends have used the
`(subject, predicate, object)` shape for decades — and over time `tiedb` has
drifted toward looking like a relational database: the `Query` API grew
`Terms`/`Exclude`/`Scope`/`Sort`/`Limit` (a `WHERE`/`ORDER BY`/`LIMIT` in
disguise), and the `Row` type hands results back as `{key, columns, values}`.
It is fair to ask whether SQLite from day one would have been the smarter call.
Both of the following are true.

**Where the triple model genuinely fits this project:**

- **Open schema.** A new relation (`artist`, `tie-type`, a tag invented
  tomorrow) needs no `ALTER TABLE` — it is just another `(key, value1, value2)`.
  For a personal media/tagging store where the "columns" are open-ended and
  user-defined, that avoids constant migrations. The SQL equivalent is an EAV
  table, which *is* a triple store with more ceremony.
- **Multi-valued by default.** "Each cell is a list" is the point: a key can
  hold many values under one relation without a join table. The common case (N
  tags per file) is the default, not the special case.
- **Reverse associations as a first-class index.** `ReverseRelations` makes
  "which files have tag X" as cheap as "which tags does file X have." SQL offers
  the same via an index on a join table; here it is the core primitive, tuned for
  exactly this query pattern (see [Associations](#associations-forward-and-reverse-indexes)).
- **Uniform storage/wire/backup.** One record shape means `dump`/`restore` is
  trivial TSV, the crash-safe writer handles one kind of record, and the FUSE
  tree derives from the same triples — no per-table schema to version.

**Where SQL would have been the easier path:**

- **We rebuilt a database.** The crash-safe writer, freespace reclamation, sync
  durability, the association index, sort/pagination — SQLite provides all of
  that for free, hardened over twenty years. That is the real cost of the DIY
  engine.
- **The `Row` type is the tell.** The moment clients wanted `{key, columns,
  values}` back, we admitted the *consumption* pattern is tabular even though the
  *storage* is triples. `Row` optimizes for the consuming client's mental model
  (a wire-friendly, language-neutral record); the triple form optimizes for the
  engine's (a subject and its predicate→object groups). `Row` sits exactly on
  that boundary, which is why the name feels slightly off from inside tiedb.
- **Ad-hoc queries.** Anything not pre-indexed in reverse is awkward; SQL's query
  planner just handles new query shapes.

**Synthesis.** A defensible alternative is *triples-as-the-logical-model over
SQLite-as-the-engine*: a single `triples(key, value1, value2)` table with the
right indexes. That keeps every conceptual benefit above (open schema,
multi-valued, reverse lookups via a second index) while deleting most of tiedb —
the writer, freespace, durability, sort. The genuinely distinctive part of the
project was never the triple store; it is the **content-addressed blob store +
FUSE tag tree** combination, and that does not care whether metadata lives in
tiedb or SQLite. None of this argues for ripping tiedb out now: it works, it is
tuned for this repo's memory constraints (memory is weighted ≥ load speed), and a
storage-engine rewrite is real risk for a mostly-ergonomic gain. It is recorded
here so the trade-off is a deliberate, remembered choice rather than an
accident.
