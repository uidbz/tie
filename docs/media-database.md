# Using tie as a personal media database

This guide shows how to run `tie` as a personal media library: import an existing
filesystem, tag whole directories (image sets, albums, TV series) and individual
files, query media by combinations of tags scoped to a media type, and record and
browse relations between media items.

It builds on the concepts in the top-level `README.md` (the triple data model,
content-addressed storage, `tiedir`) and the mount/backup sections there. Read
that first if the terms *triple*, *hash*, or *tiedir* are unfamiliar.

## The mental model

Two stores work together:

- A **filehost** (`tie-filehost`) holds the raw bytes of every file, addressed by
  a content hash. Identical content is stored once. A directory is stored as an
  immutable `tiedir` manifest whose own hash changes when its contents change, so
  directories are versioned like git trees.
- The **triple store** (`tie-triplestore`) holds everything *about* those bytes as
  `(key, value1, value2)` triples: filenames, sizes, media types, tags, the
  virtual directory tree, and relations between media items. Files are referenced
  here by their content hash — the hash is the identity of a media item.

So a media item is a content hash with a cloud of triples hanging off it. Nothing
about a file is stored inline; it is all associations, which is what makes
arbitrary tagging, cross-linking, and querying cheap.

Everything below lives in one **collection** (a namespace + collection name from
your config). Use separate collections to keep unrelated libraries apart.

## Setup

You need a running `tie-triplestore` and at least one `tie-filehost`, plus a config
that points the `tie` CLI at both. The `test-env/` directory has scripts that
stand up a local, insecure pair for experimentation:

```sh
cd test-env
./build.sh      # build tie, tie-triplestore, tie-filehost into ./bin
./start.sh      # start triplestore (:1161) and filehost (:1162), generate configs
```

For a real setup, write a config with `tie conf create` and edit it:

```toml
Username   = 'you'
Password   = 'secret'
Namespace  = 'Collections'
Collection = 'Media'                 # your library lives here
TripleStoreURL = 'https://localhost:1161'
DefaultFileHosts = ['default']

[FileHosts.default]
URL      = 'https://localhost:1162'
Insecure = false                     # true only skips TLS cert checks
```

`tie -c <config>` selects a config file (searched in the working directory
first). All commands below assume `-c` is set or a config is discoverable.

## Importing media

Import both uploads bytes to the filehost and writes the describing triples. The
CLI entry point is `tie import`, which handles files and directories of any media
type — each file's own type is detected from its contents. To label a *directory
root* as a media collection, use a dir-type subcommand (`audio-dir`, `image-dir`,
`video-dir`, `document-dir`).

### A single file

```sh
tie import ~/Music/song.flac --tags favorite --tags chill
```

This uploads the file, detects its media type from its content (not its
extension), and records — keyed by the file's content hash:

```
<hash>  filename    song.flac
<hash>  name        song            (basename without extension)
<hash>  media-type  audio/x-flac
<hash>  tie-type    audio-file      (detected: image/audio/video/document/archive)
<hash>  tie-type    file
<hash>  filesize    <bytes>
<hash>  tag-date    <timestamp>     (single-valued last-import time, µs precision)
<hash>  tag         favorite
<hash>  tag         chill
tags    all         favorite        (registry of known tags)
tags    all         chill
```

`--tags` is repeatable; each occurrence adds one tag. `--collection <name>` tags
into a specific collection instead of the config default.

### A directory tree (albums, series, image sets)

```sh
tie import audio-dir ~/Music/Album --tags jazz
```

This uploads the whole tree and **mirrors its on-disk hierarchy** as nested
virtual directories. Where the tree is *rooted* in the virtual filesystem is
decided in precedence order (see "Choosing where a tree lands" below); by default
it is rooted at the directory's **absolute path** under `file:` (the argument is
resolved with `filepath.Abs`, so relative paths and `~` expand to a full path).
Concretely, for `~/Music/Album` (say `/home/you/Music/Album`) containing

```
Album/
  cover.jpg
  disc1/
    01-intro.flac
    02-theme.flac
```

it creates:

- Virtual directories `file:/home/you/Music/Album` and
  `file:/home/you/Music/Album/disc1`, each a `DirUID` entity (a UUID) with
  `path`, `parent`, and `tie-type` triples. The root is additionally marked with
  the subcommand's dir-type (here `audio-dir`). Rooting at the absolute path means
  two directories that share a basename (e.g. `~/a/Album` and `~/b/Album`) never
  collide.
- One set of file triples per file (as in the single-file case), each with a
  `parent` pointing at the `DirUID` of its *real* containing directory. So
  `cover.jpg`'s parent is `file:/home/you/Music/Album` and the two `.flac` files'
  parent is `file:/home/you/Music/Album/disc1`.
- Each file's own media type is detected individually, so a `cover.jpg` inside an
  `audio-dir` is still tagged `image-file`.

`--tags` are applied to every file in the tree.

Built-in dir-type subcommands: `audio-dir`, `image-dir`, `video-dir`,
`document-dir`. A bare `tie import <dir>` labels the root as a generic
`directory`. Marking the type is what lets you later treat the directory as an
ordered album / series / gallery.

**Custom dir-types.** The dir-type label is a free-form `tie-type` value, so you
are not limited to the built-ins. Any key you add to the config's `[ImportDest]`
table becomes its own `import` subcommand:

```toml
[ImportDest]
podcast-dir = "/podcasts/{artist}/{title}"   # custom type, with a template
comic-dir   = ""                              # custom type, label-only
```

`tie import podcast-dir <dir>` then labels the root `podcast-dir` and roots it via
the template; `comic-dir` (empty template) just labels the root and leaves it at
the source path. The built-ins are always present even if absent from
`[ImportDest]`.

**Ordering.** Members are left in filesystem (lexical) order, which matches the
order the `tiedir` manifest already records — no per-file position triples are
written on import. Explicit ordering (for a manually reordered playlist) is a
future addition.

### Choosing where a tree lands

The virtual root of an imported directory is chosen by the first rule that
applies:

1. **Explicit `--dest`.** `tie import audio-dir ~/Music/Album --dest /music/blue`
   roots the tree at `file:/music/blue` verbatim. Highest precedence; overrides
   everything below.
2. **A per-dir-type template** from the client config's `[ImportDest]` table,
   rendered from the tree's *aggregated* metadata. Example config:

   ```toml
   [ImportDest]
   audio-dir = "/music/{artist}/{year}. {album}"
   image-dir = "/pictures/{album}"
   ```

   Importing an album of Miles Davis tracks tagged `Kind of Blue` (1959) then
   lands at `file:/music/Miles Davis/1959. Kind of Blue`. Supported variables:
   `{artist}`, `{albumartist}`, `{album}`, `{year}`, `{title}`, `{track}`
   (`{albumartist}` renders the album-artist tag, falling back to the artist —
   use it so compilations land under e.g. "Various Artists" instead of under
   whichever track artist dominates). Values are pulled from
   the files' embedded tags (e.g. ID3 for audio), extracted client-side at import
   time, and collapsed to one directory-level value each by taking the most common
   non-empty value across the tree — a stray `cover.jpg` with different tags does
   not skew placement. Each value is sanitized so a `/` in a tag (e.g. `AC/DC`)
   can't inject an extra path segment.
3. **The source's absolute path** (the default), e.g.
   `file:/home/you/Music/Album`. Used when no `--dest` is given and either no
   template is configured for the dir-type (including an *empty* template value,
   which declares a label-only custom type) *or* a referenced template variable
   is empty (so nothing lands under a half-blank path like `/music//1959. …`). This
   is also the behavior for a bare `tie import <dir>`.

Because content is addressed by hash, re-importing an unchanged tree re-uploads
nothing new and re-tags idempotently, so imports are safe to repeat — and since
the tree is just `parent`/`path` triples over content-addressed files, a tree can
later be re-placed by rewriting those triples without re-uploading a byte.

### Re-import is a sync: superseded files are versioned

Re-importing a tree whose contents *changed* is a true sync, not just an additive
re-tag. When a file's bytes change (a new content hash under the same name), or a
file is renamed/deleted on disk, `import` reconciles each directory against what
the current upload actually placed there and moves the superseded content out of
the directory instead of leaving a stale duplicate alongside the new version.

Reconciliation keys on the **content hash**, not the filename: an existing
`(hash, parent, <dir>)` edge is kept only if this import placed that hash in the
directory. Because identical bytes collapse to one content address, a single hash
can carry several filenames and live under several directories — the hash is the
only unambiguous identity.

A superseded file's old content is moved into a per-file **history directory**
named `<filename>_prev` (a real `DirUID` subdirectory of the same parent), so:

```
Album/
  cover.jpg              ← current content
  cover.jpg_prev/        ← version history for cover.jpg
    cover.jpg            ← the previous content, still browsable
```

How many versions are kept is set by the client config's `PrevVersions` (default
`3`; the oldest is dropped once the cap is exceeded). Setting `PrevVersions = 0`
disables history: the old edge is removed directly, and if that content is no
longer referenced by any directory its per-hash metadata is deleted too. Only the
`(hash, parent, <thisDir>)` edge is ever moved or deleted — content shared into
another directory (the same hash parented elsewhere) keeps its metadata and is
never touched. An unchanged file is left exactly as-is, and a `_prev` directory
that ends up empty removes itself from listings.

> **Note — `_prev` visibility in the mount.** Version retention orders by each
> file's `tag-date` (its last import time), which is stored with microsecond
> precision so rapid re-imports still order correctly. The db mount surfaces
> `tag-date` as the file's modification time (`mtime`). A `_prev` directory is
> only *visible* under the mount for recognized media types
> (image/audio/video/document), because `ReadTieDir` drops plain
> `file`/`unknown-file` children from listings; reconciliation itself works for
> any type.

> **Note — absolute-path rooting is machine-specific.** The default root (rule 3)
> is keyed on the importing machine's absolute path, which means the same logical
> tree imported from two machines lands under *different* virtual roots (file
> *content* still dedupes, since that is hash-based), and stored paths leak the
> local layout (usernames, mount points). Use a `[ImportDest]` template or
> `--dest` to root imports at a stable, machine-independent path when
> cross-machine dedup or portability matters.

### Importing a whole library (`--albums`)

`import audio-dir <dir>` treats its argument as *one* album. To import an entire
library root — hundreds or thousands of albums — add `--albums`:

```sh
# Review the plan first: one row per album, with destinations and warnings.
tie import audio-dir --albums --dry-run /music/incoming

# Then run it (asks for confirmation; -y skips the prompt).
tie import audio-dir --albums /music/incoming
```

The planner scans the tree (audio files and audio-archive blobs), clusters it
into albums, renders each album's destination, and prints the plan as a table
with per-album warnings. Nothing is uploaded or written in `--dry-run` mode.

**Grouping** is selected with `--by`:

- `auto` (default) — one directory of audio files is one album, but a directory
  whose files carry *conflicting* album tags is split per album, and
  directories that share one album identity (album + album-artist/artist tags)
  are merged — multi-disc `CD1`/`CD2` sets and scattered rips become one album.
- `dir` — strictly one directory = one album, tags only render the destination.
- `tags` — ignore the layout entirely and cluster by album tags; untagged
  files form per-directory leftover groups.

Whole-directory groups import as faithful tree mirrors (cue sheets, logs and
`artwork/` subdirectories included) via the regular `ImportDir` machinery;
merged/split/tag-clustered groups import their audio files only, preserving
the subdirectories below their common source directory (disc folders survive).

**Destinations** are rendered per album from the dir-type's `[ImportDest]`
template — or from `--dest`, which in `--albums` mode acts as an inline
template, e.g. `--dest "/music/{albumartist}/{year}. {album}"`. The
`{albumartist}` variable renders the aggregated album-artist tag, falling back
to the artist. When no template renders (e.g. an album without a year tag), a
whole-directory group falls back to its source path; a tag cluster falls back
to `<common-dir>/<album>`; a group with neither gets a warning and is skipped.
Zip/tar albums whose members are audio become single-file groups imported as
`audio-archive` blobs — no extraction needed, they read as albums directly.
(Only zip members are peeked at scan time; rar/7z/iso listings would require
streaming the whole archive.)

**Review the warnings column.** It flags: missing album/artist tags, mixed
artists with no album-artist tag, destination collisions between groups, and
albums nested inside another whole-tree import (their files appear in both).
Fix tags or prune the plan's inputs, then re-run — the plan is cheap to
regenerate, and imports are idempotent.

## Tagging after import

Tags are just triples, so you can add them with the low-level `add` command:

```sh
tie add <hash> tag summer          # tag a file
tie add tags all summer            # register the tag name (so it lists)
```

Removing a tag is a delete:

```sh
tie del <hash> tag summer
```

The tagging layer also understands a `-` prefix during import: a tag `-summer`
passed to import removes that tag instead of adding it. This is how re-imports can
retract tags.

## Finding media

### One tag (CLI)

The reverse lookup on a tag name gives every file carrying it. From Go:

```go
files, total, err := tie.FilesWithTag("favorite", 0, 100)
```

`files` is a slice of `TaggedFile{Hash, Filename, Size, IsDir}`; `total` is the
count before pagination. `offset`/`limit` paginate (limit ≤ 0 means no limit).

From the CLI you can do the same reverse query with `get`:

```sh
tie get -r -f tag favorite         # hashes tagged 'favorite'
```

### Many tags, AND / NOT, scoped to a media type

The headline query — *"find music with tag1 and tag2 but not tag4"* — combines a
media type with tag include/exclude. It is a Go client method:

```go
// audio files tagged BOTH jazz AND mellow, but NOT live
files, total, err := tie.FilesWithTags(
    client.TieAudioFile,          // media type scope
    []string{"jazz", "mellow"},   // include: must have ALL of these
    []string{"live"},             // exclude: must have NONE of these
    0, 0,                         // offset, limit
)
```

- The media type is one of `TieImageFile`, `TieAudioFile`, `TieVideoFile`,
  `TieDocumentFile`, `TieArchiveFile`.
- Pass an empty `include` to browse an entire media type (e.g. "all my videos").
- Tags in `include` are ANDed together; `exclude` removes any match carrying one
  of those tags.

The equivalent tag-only AND/NOT is available on the CLI via `get` with extra
keys (`-key` excludes):

```sh
tie get -r -f tag jazz mellow -live   # jazz AND mellow, NOT live (any media type)
```

`FilesWithTags` adds the media-type scoping on top of that, which the CLI does not
yet expose — for now media-typed queries are a Go-client feature.

> How it works: tags all share the `tag` relation, so the store intersects them
> in a single query. Media type lives under a different relation (`tie-type`), and
> the store's default set intersection keys on the relation, so it cannot be an
> AND term. Instead the media type rides along as the query's `Scope`, which the
> store intersects by associate (hash) identity alone — the whole query, scoping
> included, resolves server-side in one call.

### Faceted refinement: what tags can I further narrow by?

Once a user has picked a set of filter tags it is useful to know which *other*
tags appear on the matching files — the narrowing options left. This is the
"faceted search" pattern common in photo browsers and media managers.

```go
// User currently has "tree" and "nature" selected.
// CoTagsForQueryExcludingInput returns tags on those files *other* than the
// input tags, so the UI can offer them as additional refinements.
tags, err := tie.CoTagsForQueryExcludingInput(
    []string{"tree", "nature"}, // include: already-selected tags
    nil,                        // exclude
    "",                         // scope (empty = all media types)
)
// tags might be: ["2026", "norway", "sunset", ...]
```

`CoTagsForQuery` returns the full tag set including the input tags:

```go
// Same query but input tags are included in the result.
tags, err := tie.CoTagsForQuery([]string{"tree", "nature"}, nil, "")
```

Both calls return a sorted, deduplicated slice. `ErrNotFound` is returned when
no entries match the given include terms (the result set is empty).

> How it works: the server runs the same tag-intersection set algebra as a
> `FilesWithTags` call to find the matching hashes, then for each matching hash
> looks up its forward `tag` associations and collects every unique tag value.
> The result never crosses the wire until fully accumulated, so only the
> compact tag list — not the full file rows — is transferred.

## Relating media to each other

Any two media items can be linked by an open-vocabulary, user-named relation —
"this track sampled that one", "this scan came from that document", "this episode
follows that one". Both are identified by content hash:

```go
// this track sampled that source recording
err := tie.RelateFiles(trackHash, "sampled-from", sourceHash)
```

`RelateFiles` writes the relation in **both directions**, so it is browsable from
either item:

```go
rels, err := tie.RelationsFrom(trackHash)
// rels: []MediaRelation{{FromHash, Relation, ToHash}}
//   trackHash --sampled-from--> sourceHash

rels, err = tie.RelationsFrom(sourceHash)
//   sourceHash --sampled-from--> trackHash
```

`RelationsFrom` returns only associations whose target is itself a content hash,
so file metadata (filename, tags, …) is filtered out — you get just the
media-to-media graph for that item. Relation names are free-form; pick a
vocabulary that suits your library (`derived-from`, `cover-of`, `sequel-of`,
`cover-art-for`, …).

Both endpoints must be content hashes; `RelateFiles` rejects anything else.

Because relations are plain forward triples, you can also inspect them on the CLI:

```sh
tie get <hash>                     # shows all triples on a hash, relations included
```

## Browsing the library

### As a filesystem (live mount)

```sh
tie mount --db /mnt/media
```

mounts a live filesystem derived from the triple store, with three top-level trees:

- **`query/`** — a directory named after a tag query lists the matching files.
  `query/jazz mellow -live` ANDs `jazz` and `mellow` and excludes `live`; a
  `type:` token (e.g. `type:audio`) scopes to a media type. `cat query/tags`
  lists every known tag, and saved queries from the config's `[Queries]` table
  appear here as ready-made directories (see below). Read-only.
- **`files/`** — the path-based import tree (`file:/...`), browsable directly,
  and **writable**: `mv` renames or moves a file or a directory, and `mkdir`
  creates a directory. Reorganize your library with ordinary filesystem tools.
- **`tags/`** — mirrors `files/`, but each leaf is a **writable text file** of
  that file's tags, one per line. `cat tags/music/album/track.flac` shows the
  tags; rewriting the file re-tags the track (empty clears all its tags).

It reflects the store on every directory read, so tagging a file makes it appear
under a matching query without remounting. A tagged directory shows up as a real
directory and expands into its immutable `tiedir` snapshot. Because names and
tags are properties of the content hash, editing them affects the track wherever
it appears. See [mount.md](mount.md) for the complete mount reference.

Frequently-used queries can be saved in the config's `[Queries]` table, mapping a
friendly name to a query string. Each entry shows up as a directory under
`query/`:

```toml
[Queries]
chill-jazz = "jazz mellow -live"
recent-photos = "type:image 2024"
```

```sh
ls "query/chill-jazz"   # runs "jazz mellow -live"
```

The stored query uses the same syntax as an ad-hoc query directory (space-ANDed
terms, `-` excludes, optional `type:` scope).

### As a virtual directory tree (Go)

The mirrored hierarchy created at import is readable with `ReadTieDir`, which
returns a directory's paths, parent(s), subdirectories, and files:

```go
uid, _ := tie.DirUIDFromPath("file:/home/you/Music/Album")
dir, _ := client.ReadTieDir(tie, uid)
// dir.SubDirs, dir.Files, dir.ParentUIDs, dir.Paths
```

You can also create virtual directories directly with `MkTieDir` /
`MkTieDirAll` and mark a directory's type with `SetDirType` — the same primitives
import uses under the hood.

### Fetching bytes

A query gives you hashes; download the bytes (or a whole directory tree) with:

```sh
tie download <hash> <dest>
```

## Backup

The whole collection is triples, so backup is a TSV dump and restore is an
additive merge (see the README's *Backup / interop* section):

```sh
tie dump    > media-backup.tsv
tie restore < media-backup.tsv
```

Point `-c` at a config with a different collection to copy a library between
collections. The filehost bytes are separate — back up the filehost's data
directory (or keep the originals) to preserve the actual media.

## Tagging part of a file (planned)

A frequently wanted capability is relating a *range* of one file to a range of
another — "this 30-second passage sampled that passage", "this paragraph
originates from that page". This is **designed but not yet implemented**. The
intended model, keeping segments as derived identifiers rather than stored blobs:

- A segment id encodes its parent and range: `seg:<contenthash>:<unit>:<start>-<end>`,
  where `unit` is one of `byte`, `ms`, `frame`, `page` (e.g.
  `seg:ab12…:ms:1000-4500`). It needs no separate storage because it is fully
  derivable from the parent hash plus the range.
- Triples express the anchoring and the ranged relation, all as forward edges:
  - `(<hash>, has-segment, <segId>)` — a file lists its known segments.
  - `(<segId>, part-of, <hash>)` and `(<segId>, range, ms:1000-4500)`.
  - `(<segIdX>, <relation>, <segIdY>)` plus the reverse, mirroring the dual-edge
    trick `RelateFiles` uses so both ends browse.
- Extracting the actual bytes of a range is a filehost concern, done on demand
  from the parent hash and the range.

Until this lands, the coarsest workable substitute is to relate whole files and
note the range in the relation name or a companion triple.

## Quick reference

| Task | Command / API |
|------|---------------|
| Import a file | `tie import <file> --tags a --tags b` |
| Import a tree as an album | `tie import audio-dir <dir>` |
| Add / remove a tag | `tie add <hash> tag t` / `tie del <hash> tag t` |
| Files with one tag | `tie.FilesWithTag(tag, off, lim)` |
| Music with tags, not others | `tie.FilesWithTags(TieAudioFile, incl, excl, off, lim)` |
| Narrowing tags for a filter | `tie.CoTagsForQueryExcludingInput(incl, excl, scope)` |
| Tag-only AND/NOT (CLI) | `tie get -r -f tag t1 t2 -t3` |
| Relate two media | `tie.RelateFiles(a, "relation", b)` |
| Relations on an item | `tie.RelationsFrom(hash)` |
| Browse by tag (mount) | `tie mount --db <mountpoint>` |
| Rename / move in mount | `mv mnt/files/a mnt/files/b` |
| Make a directory in mount | `mkdir mnt/files/newdir` |
| Edit tags in mount | `printf 'a\nb\n' > mnt/tags/<dir>/<file>` |
| Read the virtual tree | `client.ReadTieDir(tie, uid)` |
| Download bytes | `tie download <hash> <dest>` |
| Backup / restore | `tie dump > f.tsv` / `tie restore < f.tsv` |
