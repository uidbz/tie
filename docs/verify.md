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

Bare `tie verify` scans the whole collection (both the `directory` and `file`
universes, unpaginated) and reports:

| Check | Meaning | Auto-repaired? |
|-------|---------|----------------|
| **Orphaned directories** | a DirUID (other than the root) with no `parent` edge | yes, by `--repair` |
| **Orphaned files** | a content hash with no `parent` edge | yes, by `--repair` |
| **Dangling parent references** | an edge `(child, parent, P)` where `P` no longer exists as a directory | no (reported only) |
| **Parent cycles** | a directory that is its own ancestor | no (reported only) |
| **Duplicate path claims** | more than one DirUID holding the same `path` | no (reported only) |
| **Missing metadata** | a file lacking `filename`/`filesize`/`media-type` or a concrete tie-type — the triples `appendTagOps` always writes; a sign of an interrupted import | no (reported only) |
| **Missing blobs** (`--check-blobs`) | a file whose content hash is absent from the filehost (never uploaded, or reaped) | no (reported only) |

Only **orphans** are auto-repairable. The other classes are judgment calls —
merging two UIDs that claim one path, or breaking a cycle, is destructive and
deserves a human decision — so `verify` reports them and leaves them alone.

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

# Re-home every orphan under tie:/restored/<today>/, then re-verify.
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

## How repair works

`--repair` re-homes **only the orphans** from the scan:

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

- `client/verify.go` — `VerifyReport`, `Verify`, `RepairOrphans`, and the
  per-check helpers (`detectCycles`, `checkFileMetadata`, `checkBlobsExist`,
  `blobExists`).
- `cmd/tie/commands.go` — the `cmdVerify` command and report rendering.
- `cmd/tie-filehost/routes.go` + `main.go` — the `HEAD /{hash}` route and
  `StatHandler`.
- `client/verify_test.go` — integration tests against the test-env triplestore
  (clean tree, orphaned file, orphaned dir subtree, missing metadata, cycle).
