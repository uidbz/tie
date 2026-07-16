# Ideas parked during the tagging/content-addressing refocus

This file preserves designs that were removed from the codebase during the
cleanup so the concepts aren't lost. Nothing here is live code. Revisit before
re-implementing — most of these were removed because they were half-baked, not
because the underlying idea is bad.

## ObjectManager[T] — reflection-based struct ↔ triple mapping

Removed: `client/objectmanager.go` and `examples/tiedb-client-objectmanager/`.

The idea was to map a Go struct to/from triples automatically, so an app could
persist a typed object without hand-writing `Add`/`Delete` calls per field.

Shape:

- `ObjectManager[T any]` bound to a `*TieClient` and a `collectionUid` (the key
  under which the set of object UIDs is listed), with `Limit`/`Offset` paging.
- Every mapped struct needed a `Uid string` field (constant `objectUid = "Uid"`).
  Field names became the `value1` (the property), unless overridden by a struct
  tag: `getPropertyName` read `field.Tag.Lookup("tie")` and fell back to the Go
  field name. So `Name string \`tie:"name"\`` stored as `uid name <value>`.
- Type handling in `ResultToObject` / `Add` covered `string`, `int`, `float32`,
  `float64`, and `[]string` (slices → one triple per element, i.e. multi-valued
  fields fell out naturally from the set semantics).
- `GetAll` did a two-step read: `Get(collectionUid)` to list object UIDs, then a
  single `Batch` of `Get(uid)` for each — one round trip for the whole set.
- `ResultToMap` / `ResultToSlice` turned a `TripleSet` into typed objects.

The genuinely good part — **`Upsert` set-diff** (worth keeping if reintroduced):

- Read the stored object into a `TripleSet` (`origObject`).
- For each struct field value: if `origObject` already has that exact
  `(uid, property, value)`, remove it from `origObject` (so it survives) and skip
  the Add; otherwise queue an `Add`.
- After walking all fields, whatever remains in `origObject` is stale → queue a
  `Delete` for each.
- Result: one batch that adds only new values and deletes only removed ones,
  correctly handling multi-valued fields without duplicates. This is the same
  desired-vs-stored diff pattern the tagging layer should use.

Why removed: reflection-heavy, un-Go-like, and it competed with the tag-based
model we're standardizing on. Two bugs also lived here (do NOT copy them):

- `Delete` fell through to `Add(object)` when the object wasn't found
  (copy-paste from `Upsert`) — deleting a missing object silently created it.
- `Add`'s error accumulation appended the batch-level `reply.Message` instead of
  the per-request `x.Message`.

If revived: keep `Upsert`'s diff, drop the create-on-missing behavior, and
consider whether reflection is worth it vs. an explicit field-registration API.

## SimpleUpdate / SimpleUpdateUsingSet — update by value1 without knowing value2

Removed: stub `SimpleUpdate` in `tiedb/collection.go` (always returned false) plus
its large commented-out body and the commented `SimpleUpdateUsingSet`.

Intent: update the "first occurrence" of a value for a `(key, value1)` without
the caller having to supply the exact current `value2`. `Update` today requires
the old `value2`; this would have found it for you (optionally the whole set) and
supported `addOnFail` (upsert) semantics.

Open question the original author left in a comment: an assertion that
`len(set.Value1) == len(set.Value2)` — the parallel-slice representation that
predates the current `TripleSet` map model, so the idea needs re-derivation
against the current data structures before reuse. "First occurrence" is also
ill-defined given map iteration order is undefined — a real version should target
the whole set or take an explicit selector.

## SortOnNextLevel / SortByNextLevelOne — sorting graph traversals

Removed: the `SortOnNextLevel` branch in `api/get.go` (a `// TODO` no-op) and
`Collection.SortByNextLevelOne` in `tiedb/collection.go` (returned an empty
slice).

Intent: when following `GetNextLevel` (treat each value2 as a key and fetch its
triples), sort the parent results by a field from the *next* level — e.g. list
files sorted by a property that lives on the thing they associate to. Never
implemented. Would need a defined contract for how next-level values map back to
a stable ordering of the parent set.

## TieOutput — tabular CLI rendering of a result set

Removed: `client/output.go` (a lone `TODO` plus a large commented-out block; the
`TieOutput` type it operated on no longer exists).

Intent: render a query result as an aligned table for the CLI's `--table`/`-t`
flag, instead of the current one-triple-per-line TSV. `TieOutput` was a
`map[string]map[string]map[string]bool` (`key → value1 → set of value2`), the
same shape as today's `TripleSet` but with a `bool` set instead of the current
set type.

The pieces:

- `LoadTieOutput(key, value1, value2, &input, &columns)` accumulated triples into
  the nested map while discovering the column set on the fly: the first column is
  always `"keys"`, and each new `value1` seen (except `"associated"`) became a new
  column. So columns emerged from the data rather than being declared up front.
- `TieOutputToTable(input, columns)` projected the map down to just the requested
  columns.
- `Print(columns)` rendered it tab-separated, wrapping multi-valued cells in
  `[a, b, c]`.
- `ToSlices(columns)` produced column-major `[][]string` for a real table
  renderer, and sorted rows by the second column via a `ByFileName` sort adapter
  (note: `ByFileName.Len` returned `len(a[1])`, i.e. the row count of column 1 —
  fragile, and it assumed at least two columns exist).

Why removed: dead since the result model moved to `TripleSet`, never wired to a
current code path, and the "columns discovered from `value1`" approach is
awkward for wide/sparse result sets (every distinct relation becomes a column,
most cells empty). If revived: build it on `TripleSet` directly, take an explicit
column selector (and sort key) rather than inferring, and pick a table library
instead of hand-aligning with tabs.

## Commented-out Tag() convenience method

Removed: the commented `TieClient.Tag(path, tags, options, addHandler)` in
`client/tie.go`.

Intent: a one-call "upload a file and tag it" helper — upload via putlib, then
add `filename`, `media-type`, `context`, per-tag, and type-specific metadata
(e.g. video height, audio ID3 album/artist/title/track/year) triples. It
referenced a `TieAdd` callback API that no longer exists and duplicated logic now
living in `client/tag.go` (`ImportFile`/`Tag`). The type-specific enrichment
(pull ID3 tags for audio, resolution for video at import time) is the part worth
carrying forward into the canonical import path.
