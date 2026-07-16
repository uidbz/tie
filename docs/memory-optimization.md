# tiedb association-index memory optimization

This records the initiative to cut the in-memory footprint of the `tiedb`
association index, the levers that worked, and — just as importantly — the two
that were built, measured, and reverted. It is a design history, not a spec:
read it to understand *why* the current data structures look the way they do and
which ideas are dead ends, so nobody spends another week rediscovering the same
trap.

For how the structures work today, see [`internals.md`](internals.md). This
document assumes that background (the trie of unique values, the forward/reverse
association indexes, `lockedTree`, `AssociationSet`, memory-only vs disk mode).

## Why this mattered

On large metadata stores the association indexes are the RSS and GC bottleneck.
Every triple is linked from up to two indexes (forward + reverse), and the
number of index entries scales with the triple count, not the (smaller) count of
distinct values. The real benchmark database used throughout is `db1.tie`:
7.4 GiB on disk, **60,692,370 entries / 98,179,667 associations**.

Two axes matter and are weighed together — neither is sacrificed much for the
other:

- **Memory** (retained `HeapAlloc` after a full load) — the primary target.
- **Load time** (wall-clock to load `db1.tie`) — kept at least as important; a
  memory win that badly regresses load is not accepted.

The discipline throughout: **measure both axes on the real DB against the
previous committed baseline under identical conditions (same worker count, same
load-path fixes), and keep a change only if it wins or ties. Guessing that a
design "should" be better is not sufficient.**

### How to reproduce the measurements

Load-time + heap on the real DB (a full load is ~3–5 min; run with a long
timeout):

```
TIE_LOAD_DB=/home/johan/db/db1.tie \
  go test ./tiedb -run TestLoadTimeProfile -v -timeout 60m
```

Add `TIE_LOAD_CPU=/tmp/x.cpu` or `TIE_LOAD_HEAP=/tmp/x.heap` for pprof profiles.
The harness reports `load wall time` and `HeapAlloc after load` — the two numbers
every entry below cites. A synthetic per-triple matrix (`TIE_PROF=1
TestAssociationMemoryProfile`, keyed by fabricated hashes) is used for quick
@1M-triple checks, but the real DB is authoritative.

## What worked

The wins, in the order they landed. The @1M figures are from the synthetic
matrix (disk mode, reverse=all); the `db1.tie` figures are the real-DB
authoritative ones.

### 1. Lazy single-entry association subtrees

Originally a fresh inner tree was allocated for *every* distinct outer key, even
one holding a single association — pure waste when the forward index is keyed by
near-unique IDs. The fix holds the single entry inline in the set struct and
only allocates the backing structure on the second distinct key. @1M: 862 → 786
MiB.

### 2. Opt-in reverse index

Most relations are never queried in reverse (`filename`, `media-type`, `size`…),
yet each paid for a reverse node. A per-collection `reverseRelations` allowlist
(`nil` = index everything, the default) skips building reverse nodes for
un-listed relations. Measured **~29% heap saving** when the workload allows
reverse=none. This is the single biggest remaining knob, but it is
workload-dependent rather than a structural change. See "Opt-in reverse
associations" in [`internals.md`](internals.md).

### 3. Kill `interface{}` boxing — typed trees via gods v2 generics

The largest distribution-independent win. The red-black trees were third-party
`github.com/emirpasic/gods` v1, whose nodes hardcode `interface{}` keys and
values, so every key and value was heap-boxed. Migrating to gods v2 generics
gave concrete `Tree[K, V]` (later, for the outer trees, a sharded
`lockedTree[K, V]`) with keys and the `int64` positions stored unboxed.

- Inner association subtree typed to `[UniqueAssociation, int64]`: @1M 786 → 633
  MiB.
- Outer trees typed too (`lockedTree`): @1M 633 → 473 MiB.

Full @1M trajectory disk/all: **862 → 786 → 633 → 473 MiB (−45%)**,
objects/triple 21.5 → 10.0. A pprof after the migration showed *no* remaining
boxing — the heap is genuine node structs plus payload. A **fork of gods for a
tighter node layout was considered and declined**: with boxing gone, the
per-node child/parent/color pointer overhead is a small prize next to what the
two migrations already banked.

### 4. Record layout: `TYPE_HASH` / blob policy (entry axis)

Orthogonal to the association work — it attacks the *entry* trie. A 32-byte
content hash stored as 64-char hex became 3 trie chunks (worst case for a trie,
since random hashes share no prefix). An opt-in `BlobPolicy` stores such values
as a single raw 32-byte `TYPE_HASH` record, reusing the existing 50-byte frame
(no `SIZE_VALUE` change). @100k with prefix-disjoint keys: heap 75.3 → 54.0 MiB
(−28%), objects/triple 16 → 9. Opt-in via `SetBlobPolicy`; old files still parse
(the record type gates interpretation). See "Whole-value hash entries" in
[`internals.md`](internals.md).

### 5. Tiered inner `AssociationSet`

Replaced the inner red-black subtree with a size-tiered store: **inline (1 entry,
no allocation) → flat `[]assocEntry` slice (2..256) → 16-way sharded map
(>256)**. The slice tier is the compact form for the bimodal middle; the sharded
map exists only for the reverse index's shared-value hot sets that many load
workers hammer concurrently. The threshold is 256.

An earlier sharded-*only* attempt regressed badly (the fixed 16-shard array —
16 mutexes + 16 map headers, ~1 KiB — was allocated on the *second* key, taxing
the huge middle population of small sets: a 2-entry set cost 1096 B vs 152 B as a
slice). Tiering fixed that. Measured on `db1.tie`, same workers, apples-to-apples:

| inner representation | load | heap |
|---|---|---|
| red-black tree (prior committed) | 5m58s | 20.7 GiB |
| slice-tiered | **4m32s** | **18.8 GiB** |

Better on **both** axes — the slice's compactness also cut load time (less
retained heap for GC to scan during the build). Committed.

> **Load-path fixes landed alongside this** (the first sharded attempt froze the
> machine): the `rawDataToLoad` channel was buffered at 64M entries (~3.7 GB of
> buffer) → cut to 8192; the ~50 MB read buffer was reallocated *inside* the read
> loop every iteration (~16 GiB of garbage) → hoisted to one reusable buffer; and
> `debug.SetGCPercent(200)` relaxes the pacer for the load duration. These live in
> `filehandling.go` `loadPass`.

### 6. Shard the outer `lockedTree` (load-speed lever)

A CPU profile of the load showed the bottleneck was the outer trees' single
write lock, not the inner set (only ~2 of 24 cores busy). The outer `lockedTree`
was changed from a single-RWMutex gods v2 rbt into a **16-way sharded `map[K]V`**
(outer-key order is provably unused — all outer lookups are point Gets, and the
sole `ForEach`, full-collection export, re-sorts). Measured on `db1.tie` @8
workers:

| outer tree | load | heap |
|---|---|---|
| slice-tiered baseline | 4m32s | 18.8 GiB |
| **16-way sharded map** | **2m46s** | **19.4 GiB** |

Load **−39%**, heap **+3%**. The small heap regression (the map's bucket slack
vs the rbt's exact node packing) was accepted for the large speed win. This is
the **current committed baseline, commit `1be88e1`**: **2m46s / 19.4 GiB**.

## What did not work — two reverted experiments

Both attempted to shrink a Go map by storing a value *inline* instead of behind a
pointer. Both lost to the same effect. They are documented in detail because the
idea is intuitively appealing and will recur.

### Negative result #1 — `entries` value stored by value

**Idea:** the `entries` map is `lockedTree[uint64, *UniqueValue]`. Store
`UniqueValue` (32 B) by value instead of behind a pointer, to kill 60.7M separate
heap objects and make the map's backing arrays pointer-free (nothing for the GC
to scan).

**Measured on `db1.tie`: 3m5s / 20.4 GiB — worse on both axes** vs the
sharded-map baseline (2m46s / 19.4 GiB).

**Why it backfired:**
- The old 32-byte `*UniqueValue` objects packed *exactly* into Go's 32 B size
  class with zero waste, and the same object was shared by both `entries` (as the
  pointee) and `uniqueValues` (which also holds a `*UniqueValue`).
- Inlining a 32 B value into map buckets pays Go's **power-of-two bucket-growth
  slack** (a map's fill oscillates roughly 44%→87% as it doubles, so live buckets
  are often only ~half full). That slack outweighed the saved object headers.
- Storing `UniqueValue` by value in *both* `entries` and `uniqueValues` also
  loses the sharing, doubling the payload.

Reverted; kept the sharded-map-only outer change.

### Negative result #2 — inline the singleton `assocCell`

**Idea (the more carefully designed of the two).** The forward index is keyed by
near-unique entry IDs, so the overwhelming majority of the ~90M+ forward keys
hold exactly **one** association — yet each singleton paid for a separately
heap-allocated `AssociationSet` struct (~88 B, including a 24 B `RWMutex` that is
never contended for a singleton) plus the 8 B map pointer. Estimated ~9–10 GiB of
the 19.4 GiB resident heap.

The plan: replace the outer map value with a 32 B value-type union:

```go
type assocCell struct {
    inlineKey UniqueAssociation // 16 B
    inlineVal int64             //  8 B
    set       *AssociationSet   //  8 B — nil ⟺ one entry inline; non-nil ⟺ ≥2, delegate
}
```

`set == nil` means "exactly one entry, held inline in the cell" (no separate
heap object, no mutex); `set != nil` delegates to the existing tiered
`AssociationSet` unchanged for ≥2 entries. A nil pointer, not a bool flag, is the
discriminator — keeping the cell at 32 B and letting a legitimate `{0,0}` subkey
coexist safely.

**This was implemented in full and all tests passed:**
- `assocCell` value-union with `size`/`lookup`/`asSet` helpers.
- A `lockedTree.Update(key, fn)` read-modify-write primitive (map values are not
  addressable, so mutation is a callback under the shard write lock).
- Cell-aware `putAssoc` (fast path: a promoted cell delegates to its set under
  the set's *own* sharded locks, never holding the outer write lock across the
  hot reverse-index inserts) and a matching `deleteAssoc`.
- Cell-aware `getAssociations`/`getReverseAssociations` (synthesize a throwaway
  single-entry set for queries only, never during load) and
  `uniqueAssociationExists` (answer the hot Add-dedup check straight from the
  inline fields — no allocation).
- A new `TestDeleteFromPromotedCell` covering the ≥2-entry delete path.

**Measured on `db1.tie` @8 workers:**

| | load | heap |
|---|---|---|
| baseline `1be88e1` | 2m46s | 19.4 GiB |
| **assocCell inline** | **3m20s** | **18.8 GiB** |
| Δ | **+34s (+20%)** | **−0.6 GiB (−3%)** |

**Failed the measure-gate on both counts** — load regressed 20% (violating the
"must not regress 2m46s" constraint) and the memory win was only 3%, nowhere
near the hoped ~14–15 GiB. Reverted (the change was never committed).

**Why the estimate was wrong — the same trap as #1.** We *did* remove the ~88 B
struct + 24 B mutex per singleton. But widening the outer map **value** from an
8 B pointer to a 32 B cell paid the same power-of-two bucket slack across ~90M
keys, and that slack ate almost the entire object-header saving — net only
−0.6 GiB. The plan had bet this was the "opposite balance" (big object removed ≫
slack added); it was not. The +20% load time came from the 4×-larger map values
(worse cache behavior, more bytes moved on every map growth) plus the
read-then-`Update` two-lock inline path. Tellingly, 18.8 GiB is *exactly* the
pre-outer-sharding slice-tiered heap — so the cell change merely clawed back the
3% that outer-sharding had cost, at a 20% speed penalty. A bad trade.

### The lesson

**Do not inline anything larger than the existing 8 B pointer into these
~100M-key outer Go maps.** Go's map bucket layout pays power-of-two growth slack
(up to ~2×) that meets or exceeds the per-object header saving, and larger values
also slow the bulk build. Two independent experiments now confirm this. Any
future outer-map memory idea must either keep the value at ≤ 8 B (i.e. stay a
pointer) or abandon Go's builtin map layout entirely (a custom open-addressing
map — see below).

## Status: GiB-scale structural levers are exhausted

At the current baseline (`1be88e1`, **19.4 GiB / 2m46s** on `db1.tie`), the
remaining heap is **genuine unboxed payload**: ~60.7M `UniqueValue` entry objects
(32 B each, packed exactly into their size class) plus ~98.2M association records
spread across the forward and reverse indexes in compact sharded maps and slices.
There is no boxing left, and no per-key struct or mutex overhead left to remove.
Both by-value inlining attempts that tried to squeeze the maps further lost to
bucket slack.

What remains is smaller, workload-dependent, or high-risk:

- **`reverse=none` / a tighter reverse allowlist** — the biggest remaining knob
  (~29% when the workload permits), but it depends on the deployment's query
  patterns, not on a code change.
- **Blob policy (`TYPE_HASH`)** — already landed; helps only where values are
  hash-shaped, and on the entry axis rather than the association axis.
- **A custom open-addressing map for the outer trees** — the *only* path that
  could inline small values without Go's bucket slack, and thus the only way the
  `assocCell` idea could still pay off. It is a large, risky build with uncertain
  payoff and is **not** currently planned; low priority.
- **A gods node-layout fork** — already declined; no boxing remains to remove.

**Conclusion: absent a workload change (more `reverse=none`, more hash-shaped
values) or a custom map implementation, there is no known large structural memory
win left in the association index.** Further effort is better aimed at the entry
axis or at accepting the current heap.
