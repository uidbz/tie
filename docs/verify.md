# Verifying the virtual file system (`tie verify`)

`tie verify` is tie's `fsck`: a read-only consistency check over a collection's
virtual file tree, plus an explicit, narrowly-scoped repair for the one failure
class that is safe to fix automatically — orphaned nodes.

It exists because the virtual tree is held together by `parent` edges, and a
bug or crash that drops an edge makes a whole subtree silently vanish from every
path query and FUSE mount. (The pre-v0.4.3 read-before-write race — fixed in
`98fe9db` — could do exactly that during import reconciliation.) `verify` is how
you detect that damage after the fact and get the lost nodes back.

## The tree it checks

A collection's file tree is a set of nodes joined by `(child, "parent", parent)`
triples:

- **Directories** are DirUID nodes. A *live* directory node carries a `path`
  triple (`tie:/some/where`) plus `parent` and `tie-type: directory`. The tree
  root is `tie:/`, whose parent is itself.
- **Files** are content-hash nodes, linked to their containing directory by a
  `parent` edge.
- A **`tiedir` snapshot blob** is an immutable, file-addressed manifest of a
  directory's contents. It carries `tie-type: directory` and `media-type:
  inode/directory` but **no `path`** — it is structurally a *file* (it has no
  children of its own and needs a parent like any other content), linked from
  its live directory node via a `tiedir-hash` edge. `verify` classifies on the
  presence of `path`, so tiedir blobs are checked as files and never mistaken
  for orphaned directories.

A node with **no `parent` edge** is unreachable from the root — that is an
**orphan**, and it is the central thing `verify` finds and can repair.

## What it checks

Bare `tie verify` runs two scans. It first asks the triplestore to cross-check
the collection's **forward and reverse association indexes** (see *The index
check* below) — the tree scan is seeded by reverse queries, so an inconsistent
index would make it report phantoms and miss children. It then scans the whole
collection (both the `directory` and `file` universes, unpaginated) and reports:

| Check | Meaning | Auto-repaired? |
|-------|---------|----------------|
| **Index inconsistencies** | a triple visible through only one of the forward/reverse indexes (see below) | yes, by `--repair` (in memory) |
| **Orphaned directories** | a DirUID (other than the root) with no `parent` edge | yes, by `--repair` |
| **Orphaned files** | a content hash with no `parent` edge | yes, by `--repair` |
| **Dangling parent references** | an edge `(child, parent, P)` where `P` no longer exists as a directory | no (reported only) |
| **Parent cycles** | a directory that is its own ancestor | no (reported only) |
| **Duplicate path claims** | more than one DirUID holding the same `path` | no (reported only) |
| **Missing metadata** | a file lacking `filename`/`filesize`/`media-type` or a concrete tie-type — the triples `appendTagOps` always writes; a sign of an interrupted import | no (reported only) |
| **Missing blobs** (`--check-blobs`) | a file whose content hash is absent from the filehost (never uploaded, or reaped) | no (reported only) |

Only **index divergences and orphans** are auto-repairable. The other classes
are judgment calls — merging two UIDs that claim one path, or breaking a cycle,
is destructive and deserves a human decision — so `verify` reports them and
leaves them alone.

### Metadata expectations (what is *not* a problem)

The metadata check matches the real schema, so a healthy store reports clean:

- **Directories are not required to have a `filename`.** Intermediate path
  nodes created by `MkTieDirAll` (`tie:/tmp`, `tie:/`, …) exist purely to hold
  the path and carry no display name; only leaf directories get one from
  `tagDir`. A missing dir `filename` is therefore *not* flagged.
- **Tiedir blobs are exempt from the `media-type` / concrete-tie-type rule.**
  They are directory snapshots, and older tag paths label them with only the
  structural `directory` marker.

## Usage

```bash
# Read-only scan of the default collection. Exits 0 when clean, 1 otherwise.
tie verify

# Scan a specific collection.
tie verify --collection Main

# Also stat every file's content hash on the filehost (one HEAD per hash).
# Slow on a large store; off by default.
tie verify --check-blobs

# Index check only (skip the tree scan); --deep also validates every index
# position against its on-disk record.
tie verify --index
tie verify --index --deep

# Repair the index in memory, re-home every orphan under tie:/restored/<today>/,
# then re-verify.
tie verify --repair

# Restore into a specific directory instead.
tie verify --repair --dest /recovered/2026-08
```

Output goes to **stderr** (stdout stays clean for scripting), one section per
problem class found, with the affected UIDs/hashes listed. The final line is
either `OK: N directories, M files — no problems` or a non-zero exit with a
problem count, so `tie verify` drops straight into a cron job or monitoring
check:

```cron
15 3 * * *  tie verify || mail -s "tie store has problems" you < /dev/null
```

## The index check

The triplestore keeps every triple in two in-memory indexes: the **forward**
index (subject → relation → value) that `Get`/`Expand` read, and a **reverse**
index (value → relation → subject) for the relations in `ReverseRelations`,
which powers every reverse-seeded query — `tag` lookups, "all files", "children
of this directory". The forward index is the source of truth (the on-disk file
holds forward records only); the reverse index is derived from it at load and
maintained alongside it on every add/delete.

When the two disagree a triple is visible through one index only:

| Class | Symptom |
|-------|---------|
| **missing-reverse** | a forward triple with no reverse entry: the file has a `parent` edge, but the directory's reverse `parent` lookup (and so `ReadTieDir`, the FUSE mount, `verify`'s tree scan) never lists it |
| **reverse-only** | a **phantom**: reverse queries return a subject that has no forward triples (`dump` never shows it), and `Delete` used to report "did not find" so it could not be cleared |
| **position-mismatch** | both entries exist but record different on-disk slots |
| **misresolved** (`--deep`) | an index position whose on-disk record is a tombstone or a different triple |

Historically this was caused by a lost-update race when two writers created a
subject's (or a directory's) index set concurrently — including the parallel
workers of the load at **every server start**, which on a large collection
dropped a sizeable fraction of forward entries and left their reverse twins
behind as phantoms. The engine now creates index sets atomically, deduplicates
concurrent adds, verifies that every resolved record matches the index entry
that pointed at it, and lets `tie del` clear a reverse-only residue. The check
exists to find any divergence that is still present in a running server (from
before the fix, or from an unforeseen cause) and to fix it without a restart.

`verify --repair` fixes the index **in memory**: it adds the reverse entries
missing for forward triples, drops reverse-only entries, realigns positions,
and (with `--deep`) rewrites a misresolved record to a fresh slot. Nothing else
on disk changes, because the file only holds forward records; a restart would
rebuild the same consistent state. The check holds the collection's write lock
for its duration — a few seconds for the in-memory pass on millions of triples;
`--deep` reads every record through the serialized writer and can take much
longer on a large disk-backed collection, blocking writes meanwhile. Reads are
never blocked. The `CheckIndex` request is classified as a **write** request
(even without repair) for that reason.

### Field remedy: `restore --drop`

If a collection's on-disk state itself is suspect — duplicate live records for
one triple, records whose index owner is gone — the definitive remedy is a full
rebuild from a dump, which is what `tie restore --drop` is for:

```bash
tie dump > backup.tsv                 # forward triples only, from the live index
tie verify --index --repair           # make sure the dump saw a consistent index
tie dump > backup.tsv                 # re-dump if the repair changed anything
tie restore --drop < backup.tsv       # drop the collection, then re-add every triple
```

`--drop` deletes the collection's `.tie` file and in-memory index server-side
before restoring, so the result contains exactly the dumped triples in a freshly
written, tombstone-free file. The whole TSV is parsed before anything is
dropped, but drop+restore is **not atomic**: the collection is empty (and
readers see nothing) until the batch completes, so run it in a maintenance
window and keep the dump.

## How repair works

`--repair` first repairs the index (above), then re-homes **only the orphans**
from the tree scan — in that order, so a phantom is never re-homed:

1. It creates the restore directory (default `tie:/restored/<YYYY-MM-DD>/`) via
   `MkTieDirAll`, so it is a normal, browsable path node.
2. For each orphaned directory and file it adds one `parent` edge pointing at
   that directory — nothing else. The node's own metadata (filename, tags,
   media type, …) is left completely intact, so a restored file is still itself,
   just reachable again.
3. It re-runs the scan, so the reported problem count and the exit code reflect
   the **post-repair** state. A store whose only problems were orphans exits 0
   after `--repair`.

Re-parenting adds an edge, never removes one, so a stale report is harmless: if
another client already re-homed a node, `--repair` just adds a second parent.
Browse the result under `/restored/<date>/` on the live mount
(`mount --db`), decide where each node really belongs, and move it with a normal
parent-edge change.

## The filehost blob check

`--check-blobs` confirms that every file's content actually exists on the
filehost — metadata pointing at a blob that was never uploaded (or was reaped by
retention) is otherwise invisible until someone tries to read it and gets a 404.

It uses a dedicated, cheap endpoint rather than downloading anything:

- **`HEAD /{hash}`** on tie-filehost. It answers `200` (with the blob's
  `Content-Length`) when the blob is present, `404` when absent, and `400` for a
  malformed hash. Existence is checked against the **primary store**
  (`BlobPath`) directly — a blob is the durable content of record, so a
  present-only-in-cache blob still counts as existing and a reaped blob is a
  clean 404. The blob is never copied into the read cache for a stat, and no
  body is transferred, so a full-store sweep costs one filesystem `stat` per
  hash.

The client checks against the **first** entry in the active collection's
`FileHosts` (its own list when set, else the top-level `DefaultFileHosts`);
since blobs are content-addressed and identical across hosts, presence on any
one host is what matters for reachability.

## Performance & safety

- **Read-only by default.** `verify` without `--repair` changes nothing; it only
  issues `Query`/`Expand` (and, with `--check-blobs`, `HEAD`) requests.
- **Memory** is O(tree size): it holds the directory and file universes to
  cross-reference parents, the same as any full-tree client.
- `--check-blobs` is the slow part — one HTTP HEAD per file — which is why it is
  opt-in. Run the cheap structural scan routinely; add `--check-blobs` when you
  suspect blob loss (e.g. after a retention misconfiguration or disk issue).

## Implementation map

- `tiedb/checkindex.go` — `Collection.CheckIndex`, `IndexReport`, and the
  in-memory repair; `tiedb/indexconsistency_test.go` reproduces the load and
  concurrent-add races and pins the fixes.
- `api/checkindex.go` — the `CheckIndex` request; `client.TieClient.CheckIndex`
  is the Go wrapper, `TieClient.check_index` the Python one.
- `client/verify.go` — `VerifyReport`, `Verify`, `RepairOrphans`, and the
  per-check helpers (`detectCycles`, `checkFileMetadata`, `checkBlobsExist`,
  `blobExists`).
- `cmd/tie/commands.go` — the `cmdVerify` command and report rendering
  (`printIndexReport`, `printVerifyReport`).
- `cmd/tie-filehost/routes.go` + `main.go` — the `HEAD /{hash}` route and
  `StatHandler`.
- `client/verify_test.go` — integration tests against the test-env triplestore
  (clean tree, orphaned file, orphaned dir subtree, missing metadata, cycle).
