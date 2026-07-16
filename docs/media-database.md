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
- The **triple store** (`tie-daemon`) holds everything *about* those bytes as
  `(key, value1, value2)` triples: filenames, sizes, media types, tags, the
  virtual directory tree, and relations between media items. Files are referenced
  here by their content hash — the hash is the identity of a media item.

So a media item is a content hash with a cloud of triples hanging off it. Nothing
about a file is stored inline; it is all associations, which is what makes
arbitrary tagging, cross-linking, and querying cheap.

Everything below lives in one **collection** (a namespace + collection name from
your config). Use separate collections to keep unrelated libraries apart.

## Setup

You need a running `tie-daemon` and at least one `tie-filehost`, plus a config
that points the `tie` CLI at both. The `test-env/` directory has scripts that
stand up a local, insecure pair for experimentation:

```sh
cd test-env
./build.sh      # build tie, tie-daemon, tie-filehost into ./bin
./start.sh      # start daemon (:1161) and filehost (:1162), generate configs
```

For a real setup, write a config with `tie conf create` and edit it:

```toml
Username   = 'you'
Password   = 'secret'
Namespace  = 'Collections'
Collection = 'Media'                 # your library lives here
Webservice = 'https://localhost:1161'
DefaultFileHosts = ['default']

[FileHosts.default]
URL      = 'https://localhost:1162'
Insecure = false                     # true only skips TLS cert checks
```

`tie -c <config>` selects a config file (searched in the working directory
first). All commands below assume `-c` is set or a config is discoverable.

## Importing media

Import both uploads bytes to the filehost and writes the describing triples. The
CLI entry point is `tie import image` — despite the name it handles files and
directories of any media type; pass `--dir-type` to say what a directory *is*.

### A single file

```sh
tie import image ~/Music/song.flac --tags favorite --tags chill
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
<hash>  tag-date    <timestamp>
<hash>  tag         favorite
<hash>  tag         chill
tags    all         favorite        (registry of known tags)
tags    all         chill
```

`--tags` is repeatable; each occurrence adds one tag. `--collection <name>` tags
into a specific collection instead of the config default.

### A directory tree (albums, series, image sets)

```sh
tie import image ~/Music/Album --dir-type audio-dir --tags jazz
```

This uploads the whole tree and **mirrors its on-disk hierarchy** as nested
virtual directories under `file:/<dir-name>`. Concretely, for a tree like

```
Album/
  cover.jpg
  disc1/
    01-intro.flac
    02-theme.flac
```

it creates:

- Virtual directories `file:/Album` and `file:/Album/disc1`, each a `DirUID`
  entity (a UUID) with `path`, `parent`, and `tie-type` triples. `file:/Album` is
  additionally marked with your `--dir-type` (here `audio-dir`).
- One set of file triples per file (as in the single-file case), each with a
  `parent` pointing at the `DirUID` of its *real* containing directory. So
  `cover.jpg`'s parent is `file:/Album` and the two `.flac` files' parent is
  `file:/Album/disc1`.
- Each file's own media type is detected individually, so a `cover.jpg` inside an
  `audio-dir` is still tagged `image-file`.

`--tags` are applied to every file in the tree.

Valid `--dir-type` values: `image-dir`, `audio-dir`, `video-dir`, `document-dir`
(default `image-dir`). Marking the type is what lets you later treat the
directory as an ordered album / series / gallery.

**Ordering.** Members are left in filesystem (lexical) order, which matches the
order the `tiedir` manifest already records — no per-file position triples are
written on import. Explicit ordering (for a manually reordered playlist) is a
future addition.

### Importing your whole filesystem

Point `import` at a top-level directory to mirror an entire tree. Because content
is addressed by hash, re-importing an unchanged tree re-uploads nothing new and
re-tags idempotently, so imports are safe to repeat.

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

mounts a live filesystem derived from the triple store, laid out as
`by-tag/<tag>/<file>`. It reflects the store on every directory read, so tagging a
file makes it appear under its tag without remounting. A tagged directory shows up
as a real directory and expands into its immutable `tiedir` snapshot.

### As a virtual directory tree (Go)

The mirrored hierarchy created at import is readable with `ReadTieDir`, which
returns a directory's paths, parent(s), subdirectories, and files:

```go
uid, _ := tie.DirUIDFromPath("file:/Album")
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
| Import a file | `tie import image <file> --tags a --tags b` |
| Import a tree as an album | `tie import image <dir> --dir-type audio-dir` |
| Add / remove a tag | `tie add <hash> tag t` / `tie del <hash> tag t` |
| Files with one tag | `tie.FilesWithTag(tag, off, lim)` |
| Music with tags, not others | `tie.FilesWithTags(TieAudioFile, incl, excl, off, lim)` |
| Tag-only AND/NOT (CLI) | `tie get -r -f tag t1 t2 -t3` |
| Relate two media | `tie.RelateFiles(a, "relation", b)` |
| Relations on an item | `tie.RelationsFrom(hash)` |
| Browse by tag (mount) | `tie mount --db <mountpoint>` |
| Read the virtual tree | `client.ReadTieDir(tie, uid)` |
| Download bytes | `tie download <hash> <dest>` |
| Backup / restore | `tie dump > f.tsv` / `tie restore < f.tsv` |
