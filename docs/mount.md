# Mounting tie as a filesystem

This documents the two FUSE mounts tie exposes and, in detail, the **writable**
operations on the live triple-store mount: renaming/moving, `mkdir`, and the
`/tags` tree that lets you edit a file's tags by writing a text file. It
describes the code as it stands in `io/fuselib/` and the client helpers it calls
in `client/tag.go` — treat it as a map for reading those files, not a spec.

The mount uses [`github.com/hanwen/go-fuse/v2`](https://github.com/hanwen/go-fuse)
(`fs` + `fuse`). Both mounts are wired from `cmdMount` in
`cmd/tie/commands.go`.

## Two mounts

### `tie mount <hash> <mountpoint>` — content-addressed tree

Read-only. Mounts a single immutable `tiedir` blob as a directory tree, fetching
child blobs from the filehost by hash on demand. Implemented by `TieFuse` /
`node` / `bytesFileHandle` in `io/fuselib/fuselib.go`. Downloaded blobs go
through the on-disk cache (see [The read cache](#the-read-cache) below); with
`--verify` they are checked against the hash they were requested under, which is
off by default. This mount has no write path: `node.Open` returns `EROFS` for
any write intent.

### `tie mount --db <mountpoint>` — live triple-store tree

A live projection of the triple store, re-read on every directory operation, so
re-tagging or importing shows up without remounting. Implemented by `TieDBFuse`
in `io/fuselib/fuselib_db.go`; the root (`dbRoot`) exposes three top-level trees:

```
/query/<query>/<file>   files matching a tag query (read-only)
/query/tags             newline list of every known tag (read-only)
/query/types            newline list of every known dir-type label (read-only)
/files/<path>/...       the path-based import tree, file:/...  (writable: rename, mkdir)
/tags/<path>/...        mirrors /files; each leaf is a writable tag file, and each
                        directory has a writable .tags (its tags) and .type (its
                        dir-type labels)
```

`TieDBFuse` embeds a `TieFuse` (`fuseTree`) purely to reuse its filehost byte
cache and content-hash inode numbering: a file's *bytes* under `/files` and
`/query` are still served content-addressed by hash, while the *structure* comes
from the triple store.

`/query` and file-content nodes stay read-only. Everything below is about the two
writable trees.

## The read cache

Both mounts fetch file bytes from the filehost on demand through one shared
cache (`cache` in `io/fuselib/fuselib.go`), built by `NewTieFuse`. It has three
properties worth knowing when reading that file:

- **On-disk, memory-bounded.** A blob is streamed to a temp file with `io.Copy`
  (`os.MkdirTemp` dir, one file per hash), not read into a `[]byte`. Reads are
  served with `ReadAt` on the open fd, so memory use is independent of file size
  — a multi-GB file reads fine. `--cache <GB>` (default 1) is the LRU eviction
  budget, a *target* not a hard cap: `evictLocked` drops least-recently-admitted
  blobs once the budget is exceeded, but a single file larger than the whole
  budget still downloads and serves (the entry it just admitted is never
  evicted). Evicted blobs are unlinked; because the fd stays open, a reader that
  already resolved an entry keeps reading it after eviction.
- **Single-flight.** `cache.blob` registers a `cacheEntry` with a `ready`
  channel under the lock, then downloads outside the lock. Concurrent reads of
  the same hash — go-fuse dispatches `Read` across goroutines — find the existing
  entry and block on `ready`, so a file is downloaded exactly once no matter how
  many readers open it. A failed download drops the entry so a later read retries
  rather than caching the error.
- **Verification is opt-in.** With `--verify` the download streams through a
  `MultiWriter(file, highwayhash)` and rejects a hash mismatch; `listContent`
  likewise checks directory blobs. Off by default (the `verify bool` on
  `NewTieFuse` / `cache` / `config` is false), because hashing every read of a
  trusted personal filehost's media is wasted work. See the README's
  transfer-integrity section for the trust model.

`TieFuse.Close()` / `TieDBFuse.Close()` remove the temp dir; `cmdMount` defers
it, so a normal unmount (Ctrl-C → `server.Unmount()`) cleans up. A hard `kill
-9` cannot run the defer and leaves a `/tmp/tie-fuse-cache-*` dir behind.

## The triples behind the trees

The writable operations are all small edits to the tagging triples (see
[`internals.md`](internals.md) for the storage model). The relevant shapes,
written originally by `client.Tag` and `client.MkTieDir`:

- **File** — subject is the content hash:
  `(hash, "filename", "photo.jpg")`, `(hash, "name", "photo")` (extension
  stripped), `(hash, "parent", <DirUID>)`, plus `(hash, "tag", <tag>)` per tag.
- **Directory** — subject is a random 64-hex `DirUID`:
  `(uid, "parent", <parentUID>)`, `(uid, "path", "file:/a/b")`,
  `(uid, "tie-type", "directory")`.
- **Membership** is the `parent` edge on the child; a directory's children are
  found by a *reverse* lookup on `parent = <uid>` (`client.ReadTieDir`). There is
  no child-list triple.
- **Path→UID** resolution is a reverse lookup on `path` (`DirUIDFromPath`), which
  expects exactly one UID per path.

One caveat runs through all of it: **names and tags are properties of the content
hash**, not of a directory slot. If the same content appears in two directories,
they share one `filename` triple and one tag set. Renaming a file or editing its
tags in one place changes it everywhere that content appears, and in every
`/query` view. This is inherent to the content-addressed model, not a bug.

## Rename and move (`/files`)

`pathDir` implements `fs.NodeRenamer`. The kernel calls `Rename` on the *source*
directory node with the destination parent inode, so a single method handles
in-place rename, cross-directory move, and both at once.

```
Rename(ctx, name, newParent, newName, flags)
```

Flag and target handling:

- `RENAME_EXCHANGE` → `ENOSYS` (atomic swap is unsupported).
- `newParent` that is not a `*pathDir` (e.g. a target in `/query`) → `EXDEV`, so
  `mv` reports a clean cross-device error instead of corrupting state.
- `RENAME_NOREPLACE`, or any collision with an existing destination entry →
  `EEXIST` (`destExists` / a `DirUIDFromPath` probe for dirs).

The source entry is located by reading the source directory (`d.read()` →
`ReadTieDir`) and matching `name` against files then subdirs. Two cases:

### File rename / move — `client.RenameFile`

`RenameFile(tie, hash, oldParent, newParent, newName)`:

- If the name changed, `Update` the `filename` triple and the `name` triple
  (extension stripped, mirroring `Tag`). `AddOnFailure` is set so a file that
  never recorded a filename (the hash-fallback case in `ReadTieDir`) still gets
  one.
- If the parent changed, `Delete(hash, "parent", oldParent)` +
  `Add(hash, "parent", newParent)`. It deletes the *specific* old edge rather
  than blanket-updating, because `parent` is multi-valued — the same content can
  live in several directories, and a move must not disturb the others.

Same-directory rename touches only the name triples; a move touches only the
parent edge; a move-and-rename does both in one `Batch`. Then `Sync`.

### Directory rename / move — `client.RenameDir`

A directory's place in the tree *is* its `path` triple, and every descendant
directory carries the renamed prefix in its own `path`, so the rename cascades:

- `collectDescendantDirs` walks the subtree breadth-first via `ReadTieDir`.
- One `Batch` rewrites the directory's own `path` (`oldPath` → `newPath`) and, for
  each descendant, replaces the `oldPath` prefix of its `path` with `newPath`.
- On a move, the top directory's `parent` edge is swapped (Delete + Add); the top
  dir only — descendants keep their parents.

Files need **no** path rewrite: they have no `path` triple, and their `parent`
DirUID is unchanged by an ancestor rename. Then `Sync`.

## `mkdir` (`/files`)

`pathDir` implements `fs.NodeMkdirer`. `Mkdir` probes `DirUIDFromPath` for a
collision (`EEXIST`), then calls `client.MkTieDir(file:/<parent>/<name>)`, which
mints a `DirUID` and writes its `parent` / `path` / `tie-type` triples. After
`Sync`, it returns a fresh `pathDir` inode so the new directory is immediately
listable and can be populated by further `mkdir`s.

## The `/tags` tree — editing tags as text files

`tagsDir` mirrors `pathDir`'s navigation exactly (resolve path → DirUID, list
children by the reverse `parent` lookup), but its **leaves are `metaFile` nodes**:
small writable text files whose contents are the underlying content's tags, one
per line. Each directory additionally exposes two `metaFile` control files bound
to the directory's own `DirUID` — **`.tags`** (its tags) and **`.type`** (its
dir-type labels) — see "A directory's own tags and type" below.

- **Read** (`cat /tags/music/song.mp3`) returns the current tags, sorted, via
  `client.GetTags(hash)` — the `(hash, "tag", *)` values.
- **Write** replaces the whole tag set. On flush the buffer is parsed
  (one tag per line, blanks trimmed) and `client.SetTags(hash, tags)` computes
  the minimal diff against the stored tags: tags added get `Add(hash, "tag", t)`
  plus the `(tags, "all", t)` registry entry that `Tag` writes; tags removed get
  `Delete(hash, "tag", t)`. Then `Sync`. Emptying the file removes all tags.

Because tags attach to the hash, an edit here is visible under every `/query`
view immediately.

### One node type, two backing sets — `metaFile`

`.tags` (on a leaf or a directory) and `.type` (on a directory) are the same
`metaFile` node: a writable text file whose contents are a sorted set of strings,
one per line, with a truncate-safe rewrite path. A `metaFile` differs only in two
closures — `load` and `store` — bound at construction:

- `newTagFile(state, hash)` → `client.GetTags` / `client.SetTags` on a subject
  (a content hash *or* a `DirUID` — both key the same `(subject,"tag",*)` shape).
- `newTypeFile(state, uid)` → `client.GetDirType` / `client.SetDirTypes` on a
  `DirUID`, editing its `(uid,"tie-type",*)` classification labels.

So read, full-rewrite, and clear behave identically for both; only the triples
touched on flush differ.

### A directory's own tags and type — `.tags` and `.type`

Every `/tags/<path>/` directory contains two dotfiles bound to that directory's
`DirUID` rather than to any child's content hash:

- **`.tags`** — the directory's own tags. A tagged directory carries
  `tie-type directory` + `tag` triples on its UID; `.tags` edits the `tag` set
  exactly as a leaf does.
- **`.type`** — the directory's dir-type classification labels (`audio-dir`, or
  any custom label like `live-album`). `GetDirType` returns the `(uid,"tie-type",*)`
  values **with the structural `directory` marker filtered out**, and
  `SetDirTypes` diffs against those, **never adding or removing `directory`** — so
  a text-editor rewrite of `.type` can't drop the marker that `ReadTieDir` uses to
  tell subdirs from files. Added labels also get a `(types, "all", label)`
  registry entry (the analogue of the tags registry), which `/query/types` reads
  back. Directory type labels do **not** cascade to descendants.

`tagsDir.Lookup` resolves the fixed names `tagsSelfName` (`.tags`) and
`tagsTypeName` (`.type`) to `d.read().Uid`; `Readdir` lists both in every
directory (including the root and childless dirs, where they may read empty).
Dotfile names are used deliberately: a child file literally named `.tags`/`.type`
would be shadowed. The two share one `DirUID` but need distinct inodes, so `.type`
keys its inode on `typeInodeKey(uid)` (`uid + "\x00type"`) while `.tags` keys on
the bare `uid`.

### The write path in detail (and the ATOMIC_O_TRUNC gotcha)

Tools rewrite a whole file to "set" its contents: shell redirection, `tee`, and
editors all **truncate to zero, then write**. `metaFile` models exactly that:

- `Open` for read snapshots the current values into the handle buffer. `Open` for
  write starts from an **empty** buffer — a meta file is always a full rewrite, so
  the bytes written *are* the new set. `FOPEN_DIRECT_IO` is returned so the
  kernel doesn't serve a stale cached size between the truncate and the rewrite.
- `metaFileHandle.Write` copies incoming bytes into the buffer at the given
  offset.
- `Flush` (on `close(2)`) commits: any writable handle commits its buffer via the
  node's `store` closure (`SetTags` / `SetDirTypes`), even if zero bytes were
  written, so `: > file` clears the set.

The subtle part is the truncate. go-fuse does **not** advertise the
`ATOMIC_O_TRUNC` capability by default (checked through v2.11.0 — the capability
mask in `fuse/opcode.go`'s init handler omits it; v2.11.0 adds an opt-in
`ExtraCapabilities` server option). Without it, the kernel strips `O_TRUNC` from
the open flags and instead issues a separate node-level `SETATTR(size=0)` *before*
`Open`. So `metaFile` implements `fs.NodeSetattrer` and accepts the size change
there; the truncate never reaches an open handle. This is why the truncate is
handled at the node level rather than via an `O_TRUNC` branch in `Open` — it makes
the behavior independent of whether ATOMIC_O_TRUNC is ever negotiated. Non-size
`Setattr` calls (chmod/utimes from tools) are accepted as no-ops so they don't
fail.

## Error mapping

Client-layer errors surface as `EIO`; structural rejections use specific errnos
(`EXDEV`, `EEXIST`, `ENOSYS`, `ENOENT`) so shell tools behave sensibly. All
mutations call `Sync` before returning success, so a subsequent `ls`/`cat`
observes the change (the store's `Sync` is the durability barrier — see
[`internals.md`](internals.md)).

## Verify

Against the local test environment (`test-env/`):

```sh
cd test-env && ./build.sh && ./start.sh
./bin/tie -c config.toml mount --db mnt &     # or ./mount-db.sh
#   add --cache <GB> to size the on-disk read cache (default 1)
#   add --verify to hash downloaded bytes against their address (default off)
ls mnt/                                        # query  files  tags

# rename + move (writes filename / parent triples)
mv "mnt/files/<dir>/a" "mnt/files/<dir>/b"
mv "mnt/files/<dir>/b" "mnt/files/<other>/"

# directory rename (cascades path over descendants)
mv "mnt/files/<dir>" "mnt/files/<newname>"

# mkdir
mkdir "mnt/files/<dir>/new"

# edit tags
cat  "mnt/tags/<dir>/<file>"                   # current tags, one per line
printf 'jazz\nmellow\n' > "mnt/tags/<dir>/<file>"   # replace the set
: > "mnt/tags/<dir>/<file>"                     # clear all tags

# edit a directory's own tags
cat  "mnt/tags/<dir>/.tags"                    # the directory's tags
printf 'album\nfavorite\n' > "mnt/tags/<dir>/.tags"

# edit a directory's own dir-type labels (the structural "directory" marker is
# preserved; added labels register in /query/types and become type: queryable)
cat  "mnt/tags/<dir>/.type"
printf 'audio-dir\nlive-album\n' > "mnt/tags/<dir>/.type"
cat  mnt/query/types                           # every known dir-type label
ls   "mnt/query/type:live-album"               # dirs carrying the custom label
```

Confirm the effect with `tie dump` (the `filename` / `parent` / `path` / `tag`
triples, and `tie-type` for `.type`) and by checking that `/query/<tag>` reflects
tag edits. After a `.type` edit, `tie dump | grep <DirUID>` must still show the
`tie-type directory` marker alongside the edited labels. `/query` must reject
writes (`mv` there returns "Operation not supported").

## Future work

- **Creating files through the mount.** `mkdir` creates directory nodes, but
  there is no `Create`/`Write` path for new *files* — that needs a filehost
  upload plus tagging, a larger change. `touch`/copy-in are unsupported.
