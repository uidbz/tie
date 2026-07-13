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
`datastructures.go`):

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

- `associations`: `key (uint64)` → subtree of
  `UniqueAssociation{AssociateTo: value2, Relation: value1}` → `pos (int64)`
- `reverseAssociations`: `value2 (uint64)` → subtree of
  `UniqueAssociation{AssociateTo: key, Relation: value1}` → `pos (int64)`

`pos` is the byte offset of that triple's record in the on-disk file (or `-1`
before it has been written). Both indexes point at the *same* record; the reverse
index is a pure in-memory acceleration structure and is never persisted
separately — it is rebuilt from the file on load.

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

Trade-off: on `tie-daemon`, generic reverse queries (`tie get -r <x>`, reverse
intersect/exclude) resolve only for whitelisted relations. Collections that
never set an allowlist keep full reverse generality.

### Notes for future work

- The reverse index is derived state — it can always be rebuilt from the file,
  which is what makes the two-pass load and the opt-in filter safe.
- Boxing: rbt keys/values are stored as `interface{}`, so each
  `UniqueAssociation` key and each `int64` position is heap-boxed. Reducing this
  (e.g. typed trees or value-packed keys) is the next lever if association
  memory is still the bottleneck after the opt-in reverse win.
- A lazy reverse index (build a value's reverse subtree on first reverse query
  and cache it) was considered as an alternative that keeps full generality at
  the cost of an O(n) first query; the opt-in approach was chosen because this
  codebase's reverse queries hit a small, known set of relations.
