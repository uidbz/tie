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
`node` / `bytesFileHandle` in `io/fuselib/fuselib.go`. Every downloaded blob is
verified against the hash it was requested under before it is cached or served
(the filehost is untrusted — see the README's transfer-integrity section). This
mount has no write path: `node.Open` returns `EROFS` for any write intent.

### `tie mount --db <mountpoint>` — live triple-store tree

A live projection of the triple store, re-read on every directory operation, so
re-tagging or importing shows up without remounting. Implemented by `TieDBFuse`
in `io/fuselib/fuselib_db.go`; the root (`dbRoot`) exposes three top-level trees:

```
/query/<query>/<file>   files matching a tag query (read-only)
/query/tags             newline list of every known tag (read-only)
/files/<path>/...       the path-based import tree, file:/...  (writable: rename, mkdir)
/tags/<path>/...        mirrors /files, but each leaf is a writable tag file
```

`TieDBFuse` embeds a `TieFuse` (`fuseTree`) purely to reuse its filehost byte
cache and content-hash inode numbering: a file's *bytes* under `/files` and
`/query` are still served content-addressed by hash, while the *structure* comes
from the triple store.

`/query` and file-content nodes stay read-only. Everything below is about the two
writable trees.

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
children by the reverse `parent` lookup), but its **leaves are `tagFile` nodes**:
small writable text files whose contents are the underlying content's tags, one
per line. Directories are pure navigation — a directory has no tag file of its
own (see "Future work").

- **Read** (`cat /tags/music/song.mp3`) returns the current tags, sorted, via
  `client.GetTags(hash)` — the `(hash, "tag", *)` values.
- **Write** replaces the whole tag set. On flush the buffer is parsed
  (one tag per line, blanks trimmed) and `client.SetTags(hash, tags)` computes
  the minimal diff against the stored tags: tags added get `Add(hash, "tag", t)`
  plus the `(tags, "all", t)` registry entry that `Tag` writes; tags removed get
  `Delete(hash, "tag", t)`. Then `Sync`. Emptying the file removes all tags.

Because tags attach to the hash, an edit here is visible under every `/query`
view immediately.

### The write path in detail (and the ATOMIC_O_TRUNC gotcha)

Tools rewrite a whole file to "set" its contents: shell redirection, `tee`, and
editors all **truncate to zero, then write**. `tagFile` models exactly that:

- `Open` for read snapshots the current tags into the handle buffer. `Open` for
  write starts from an **empty** buffer — a tag file is always a full rewrite, so
  the bytes written *are* the new tag set. `FOPEN_DIRECT_IO` is returned so the
  kernel doesn't serve a stale cached size between the truncate and the rewrite.
- `tagFileHandle.Write` copies incoming bytes into the buffer at the given
  offset.
- `Flush` (on `close(2)`) commits: any writable handle commits its buffer via
  `SetTags`, even if zero bytes were written, so `: > file` clears all tags.

The subtle part is the truncate. go-fuse does **not** advertise the
`ATOMIC_O_TRUNC` capability by default (checked through v2.11.0 — the capability
mask in `fuse/opcode.go`'s init handler omits it; v2.11.0 adds an opt-in
`ExtraCapabilities` server option). Without it, the kernel strips `O_TRUNC` from
the open flags and instead issues a separate node-level `SETATTR(size=0)` *before*
`Open`. So `tagFile` implements `fs.NodeSetattrer` and accepts the size change
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
```

Confirm the effect with `tie dump` (the `filename` / `parent` / `path` / `tag`
triples) and by checking that `/query/<tag>` reflects tag edits. `/query` must
reject writes (`mv` there returns "Operation not supported").

## Future work

- **`.tags` inside directories.** Directories are navigation-only today. A tagged
  directory (a dir hash carrying `tie-type directory` + `tag` triples) could
  expose a `.tags` file *inside* itself to make its own tags editable, the same
  way leaf files work. Deferred past the first cut.
- **Creating files through the mount.** `mkdir` creates directory nodes, but
  there is no `Create`/`Write` path for new *files* — that needs a filehost
  upload plus tagging, a larger change. `touch`/copy-in are unsupported.
