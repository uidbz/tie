# Changelog

All notable changes to this project are documented here. The format is loosely
based on [Keep a Changelog](https://keepachangelog.com/), and the project aims
to follow semantic versioning.

## [Unreleased]

### Fixed

- **tie-filehost example config: table sections moved to the bottom.** The
  commented `[[BlobPaths]]` block sat above `ReapInterval`/`Insecure`/cache and
  auth keys, so uncommenting it in place made TOML absorb every key below into
  the last store entry — silently ignoring e.g. `Insecure = true` (the server
  then demanded TLS certs) and `CachePath`. The example now keeps all plain
  `key = value` settings above the `[[Users]]`/`[[BlobPaths]]` sections, with a
  note explaining the ordering rule.

## [v0.5.0] - 2026-09-01

### Added

- **`tie stat` — human-readable info for a path or hash.** Reports type, size,
  and metadata for a virtual path or content hash; for directories it sums child
  sizes recursively from the `size` triples rather than walking blobs.
- **Query sort-by-attribute.** `QuerySpec` can sort matched keys by the value of
  a chosen attribute, ascending or descending, so clients get ordered results
  without a client-side sort pass.
- **`DeleteTable`** removes a table entity and all of its rows in one call,
  completing the table API (`InsertTable`/`ReadTable`). Mirrored in the Python
  client.
- **Audio duration at import.** Imports now extract track duration (via a fork
  of `dhowden/tag`) and, for imported directory nodes, aggregate `album`,
  `artist`, and `year` from their children onto the directory record.
- **Filehost content-type sniffing.** `tie-filehost` infers a blob's
  `Content-Type` from its leading magic bytes when serving it, while staying a
  pure content-addressed byte store (no format parsing beyond the sniff).
- **`tie verify` — a consistency check (fsck) for the virtual file tree.** Scans
  a whole collection and reports lost or incomplete nodes: orphaned
  directories/files (no `parent` edge, so unreachable from the root), dangling
  parent references, parent cycles, duplicate path claims, files missing core
  metadata, and — with `--check-blobs` — files whose content is absent from the
  filehost. Bare `verify` is read-only and exits non-zero when any problem is
  found, so it is cron-able. `verify --repair` re-homes only the orphans under
  `tie:/restored/<date>/` (one added `parent` edge each; all other metadata
  untouched) and re-scans so the exit code reflects the post-repair state; every
  other problem class is reported but never auto-fixed. A `tiedir` snapshot blob
  carries `tie-type: directory` but no `path`, so the scan partitions on the
  `path` triple and checks such blobs as files, not orphaned dirs. See
  docs/verify.md.
- **`HEAD /{hash}` on tie-filehost** — a cheap blob-existence check backing
  `verify --check-blobs`: 200 with the blob's `Content-Length` when present,
  404 when absent, 400 for a malformed hash. It stats the primary store
  (`BlobPath`) directly, so it never copies into the read cache and transfers no
  body — one filesystem stat per hash for a full-store sweep.

### Changed

- **Multi-collection client and multi-store filehost.** One client config can
  now bind several collections via `[Collections.<name>]` entries (each may
  override `Namespace`, `Collection`, `TripleStoreURL`, credentials, and
  `FileHosts`), with `DefaultCollection` selecting the one used when none is
  named; a flat config with no `[Collections]` is normalized into a single
  synthesized entry, so old configs keep working. On the server, `tie-filehost`
  gained `[[BlobPaths]]` to run several physical stores in one process: uploads
  route to a store by the `Tie-Store` header, dedup and retention are per-store,
  and the expired-blob reaper computes directory protection globally so a parent
  blob in one store shields children in another. The single-store `BlobPath` key
  is retained as a deprecated form.
  - **CLI flag swap:** `-c`/`--collection` now selects the collection and
    `-C`/`--config` selects the config file (previously reversed). `-C` also
    loads the config from an explicit path.
- **Renamed the `tie-daemon` server to `tie-triplestore`.** The binary,
  `cmd/` directory, config file (`tie-triplestore.toml`), and service units
  (`tie-triplestore.service`, `openrc/tie-triplestore`) all follow the new name,
  which describes what the process *is* (the HTTP triple-store server) and mirrors
  its sibling `tie-filehost`. The client config key `DaemonURL` is renamed to
  **`TripleStoreURL`**; the older `Webservice` key is still honored as a
  deprecated alias, but the short-lived `DaemonURL` key is removed. Update any
  service files, config keys, and the `TIE_TRIPLESTORE_DB` backup env var
  accordingly.
- **Module path migrated** from sourcehut to `github.com/uidbz/tie`, and the
  `conf` configuration dependency switched to `github.com/uidbz/conf`. Update
  import paths and any `go get` references accordingly.

## [v0.4.3] - 2026-08-21

### Added

- **First-class table insert/read API.** `TieClient.InsertTable(uid, headers,
  rows)` and `ReadTable(uid)` store a rectangular grid of string cells as a table
  entity and read it back, so tabular data (Excel sheets, CSVs) no longer needs a
  hand-rolled triple encoding per script. Built purely on the existing
  Query/Set/Get/Expand/Batch primitives — no server changes. Column and row order
  are stored as ordinal-prefixed list values (the store returns a subject's
  multi-values sorted, not in insertion order). An empty uid mints a fresh one; a
  supplied uid replaces in place (idempotent re-import). Headers must be unique
  and none may be named `tie-type` (it would collide with the row type marker);
  either case is rejected with an error. Mirrored in the Python client
  (`insert_table`/`read_table`).
- **Pure-stdlib Python client and CLI** (`python/`), mirroring the Go
  `TieClient` facade (triples, queries, batches, files, tags, dirs, imports,
  versions, favorites, relations) with no third-party dependencies.
- **Favorite-tags API.** `RegisterFavorite`/`UnregisterFavorite`/`ListFavorites`
  mark tags as favorites via a `(favorite,"all",…)` registry.
- **Writable file path.** FUSE `Create` and edit-save now write blobs back
  through the filehost and tag them; superseded content is versioned into the
  `_prev` history collection.
- **Shell-completion command** exposed in `tie` help (`completion
  <bash|zsh|fish|pwsh>`).

### Changed

- **File version-history moved to an isolated `<Collection>_prev` collection**,
  powered by a new `version-of` default reverse relation; adds `tie versions
  list/restore`.
- **Virtual-path scheme switched from `file:` to `tie:`** for the internal
  tag/path tree (a private URI scheme, not RFC 8089 `file:`).
- **Imports survive large directories** by chunking oversized requests.

### Fixed

- **tiedb read-before-write race** that could silently drop triples.
- **FUSE 0-byte writes** on truncating opens and empty path-tree roots.
- **Duplicate-UID path handling** (with a `contrib` dedup script).

## [v0.4.2] - 2026-08-16

### Added

- **`tie tag` command group for tag management.** A new CLI command groups tag
  operations: `list` (registered tag names), `add <tag>` (register a name in the
  `(tags,"all",…)` registry without needing a file), `del <tag>` and
  `rename <old> <new>` (remove/rewrite a tag on every item and the registry,
  globally), `files <tag>` (items carrying a tag), `show <hash>` and
  `set <hash> [tags…]` (view/replace an item's tags), and `untagged
  [--type <tie-type>]` (see below). Because tags attach to a content hash,
  `del`/`rename`/`set` change the tag everywhere that content appears. Global
  rewrites are applied in chunked batches so a tag spanning a large store never
  builds one oversized request. Backed by new client methods `RegisterTag`,
  `DeleteTag`, `RenameTag`, and `UntaggedFiles`.

- **`MissingRelation` query predicate ("has no X").** `QuerySpec.MissingRelation`
  (wired through `api` and `tiedb.TagQuery`) keeps only matches that carry no
  triple under a named relation — the negation-of-existence the association
  algebra's `Exclude` cannot express (it removes a specific value, not the
  presence of a relation). It is resolved entirely server-side: after the normal
  Include/Exclude/Scope pass, only subjects whose forward index lacks the relation
  survive, via per-subject forward point-lookups (`AssociationSet.HasRelation`).
  Cost is O(candidates) lookups with O(result) memory — no full-collection scan
  and no client-side download. `tie tag untagged` uses it with
  `MissingRelation="tag"` scoped to a `tie-type` to list files/directories that
  carry a tie-type but no tag; any client (e.g. an image viewer) can pass the
  field on an ordinary query to page untagged items server-side.

- **Read/read-write user roles and filehost authentication.** Both `tie-daemon`
  and `tie-filehost` now share a role-based access model (new `auth` package):
  users are `read` (queries / downloads) or `write` (full access). `[[Users]]`
  gained a `Role` field (defaults to `write`, so existing configs are
  unaffected), and a new `AnonymousAccess` key (`"none"` | `"read"` | `"write"`)
  sets what an unauthenticated request may do.
  - `tie-filehost` had **no authentication** before; it is now opt-in and
    **defaults to fully open** (`AnonymousAccess = "write"`) so anonymous uploads
    keep working. Set `"read"` to require a write user for uploads (downloads
    stay open) or `"none"` to require auth for everything, and add `[[Users]]`.
  - `tie-daemon` continues to default to always-authenticated
    (`AnonymousAccess = "none"`); its request set is now classified read vs write
    and gated per role.
  - Rejections return `401` for missing/invalid credentials and `403` for a valid
    user whose role is too low. Passwords stay plaintext in config, but the wire
    comparison is now constant-time.
  - Client configs may set `Username`/`Password` per `[FileHosts.<name>]`; the
    credentials are sent as HTTP Basic Auth on every upload and download.

## [v0.4.1] - 2026-08-06

### Added

- **Faceted tag refinement (`CoTagsForQuery`).** A new daemon endpoint
  (`POST /CoTags`) and matching client methods return all unique tags carried by
  entries that match a given AND/NOT/scope tag query. This is the "what can I
  narrow by next?" query: given the user's current filter (e.g. `tree,nature`),
  it answers which further tags (e.g. `2026,norway,sunset`) exist on the
  matching set — without transferring full file rows. Use
  `TieClient.CoTagsForQueryExcludingInput` to strip the input tags from the
  result, or `CoTagsForQuery` to include them.

- **Versioned re-import.** Re-importing a directory tree is now a true sync:
  when a file's content changes (a new hash under the same name) or a file is
  renamed/deleted on disk, its superseded content is moved into a per-file
  `<filename>_prev` history directory instead of lingering as a stale duplicate.
  A new client-config `PrevVersions` (default 3) bounds how many versions are
  kept per file, oldest dropped first; `0` disables history and deletes the old
  edge outright (garbage-collecting content no longer referenced anywhere).
  Reconciliation keys on the content hash, so shared content (the same hash in
  another directory) is never disturbed.
- **File modification times in the `--db` mount.** Files now report their import
  date as `mtime`, surfaced from the `tag-date` triple.
- **LRU blob cache for tie-filehost.** When `CachePath` is set in the filehost
  TOML config, blobs are copied from `BlobPath` (slow storage) into `CachePath`
  (fast storage, e.g. SSD or tmpfs) on first access and served from there
  afterward. Concurrent requests for the same hash coalesce on a per-entry ready
  channel (single copy), and LRU eviction enforces the `CacheSizeGB` budget;
  both download handlers fall back to `BlobPath` on any cache error.
- **`restore --drop`.** The CLI `restore` command gains a `--drop` flag that
  drops the target collection before restoring, so the backup's data replaces
  the collection entirely rather than merging into it. Backed by a new
  end-to-end `Drop` request (`TieTree.DropCollection` /
  `TieClient.DropCollection`).

### Changed

- **Flat-`Row` client API.** Queries and lookups now return flat, ordered
  `client.Row`s (`{key, attributes: {relation -> []value}}`) — the same JSON
  shape a non-Go client parses. The client surface is `Get` (one key's
  attributes), `Query` (tag/association search with ordering and
  `Offset`/`Limit` pagination), `Expand` (many keys in one round trip), and
  `Set` (replace a relation's values in one op). This replaces the old
  `Get(key, GetOptions)` / `SimpleGet` triple-set API. The single-value row
  helper `RowOne` is gone; use `RowValues`/`RowFirst`.
- **`tag-date` is single-valued and microsecond-precision.** Re-tagging a hash
  no longer accumulates multiple `tag-date` values; it records only the last
  import time. The finer precision keeps version retention ordered correctly for
  re-imports within the same second. Legacy second-resolution values still parse.
- **filehost config key `DbPath` renamed to `BlobPath`.** The filehost's blob
  storage path is now `BlobPath` in the TOML config, disambiguating it from the
  daemon's (unchanged) `DbPath`. Existing filehost configs must rename the key.
- **`make install` installs only the client.** The rootless common case,
  `make install`, now installs just the `tie` client into `GOBIN`. The full
  server install (client + daemon + filehost + system services) moves to
  `make install-server`.
- **`-c` config name accepts an omitted `.toml` extension.** `tie -c myconf`
  now resolves to `myconf.toml`; the extension is appended when absent in both
  load and save paths.

## [v0.4.0] - 2026-07-24

The largest release so far: a hardened, crash-safe write path in tiedb, a
concurrent daemon, a writable FUSE tag tree with a disk-backed read cache, and a
reworked CLI. All three binaries now report a build version (`tie --version`,
`tie-daemon -version`, `tie-filehost -version`).

### Added

- **Embedded version string.** All binaries report their build version,
  injected from the git tag via `make build` / `make release`.
- **`mount --db` is now writable.** Rename and mkdir are supported, plus a live
  `/tags` tree; config-saved queries surface as `query/` directories, and each
  directory carries an editable tie-type via `.type` backed by a
  `/query/types` registry.
- **Disk-backed single-flight read cache** for FUSE, with an LRU `--cache`
  budget and opt-in `--verify` for byte verification against the content
  address (off by default for trusted filehosts).
- **Structured logging (`tielog`)**: JSON to a log file plus pretty stderr.
- **Per-collection `ReverseRelations` overrides** in the daemon TOML config.
- **Per-blob retention and ownership** on tie-filehost, TOML-configured, with an
  expired-blob reaper.
- **Media database workflow**: media-typed queries and relations,
  config-driven, metadata-driven import placement, and server-side media-type
  scope intersection in tag queries.
- **`tie dump --file`** to export directly from a local `.tie` file.
- **Upload/download progress bars.**
- **`QueryTags` API** and lazy association sub-trees in tiedb.
- **contrib**: service installers (systemd/OpenRC), a combined Docker image, and
  a filehost/daemon storage-layout + backup guide with scripts.

### Changed

- **CLI migrated to urfave/cli v3** with shell completion, real usage/description
  text, and `import` reshaped into readable dir-type subcommands.
- **`tie upload`/`download`** replace the standalone `tie-upload`/`tie-download`
  binaries; there is no `put`.
- **Daemon serves requests concurrently** instead of through a single
  serializing worker.
- **Webservice uses stdlib `net/http`** with TOML-based access management
  (replacing the external UID service and account-creation flow).
- **`dump` streams as NDJSON** with proper TSV quoting.
- **tiedb internals migrated to gods v2 generics**; the outer index is sharded
  into a 16-way locked map, and the association subtree uses a size-tiered store.
  Content hashes are stored as whole-value entries via an opt-in blob policy;
  reverse-association indexing is opt-in per relation.
- **Client is config-driven**: TLS via a `FileHost` struct, an
  `Upload`/`Download` facade, local UUIDv7 instead of an external UID service,
  and collection passed per-import.
- **Build system**: root shell scripts replaced by a `Makefile` (documented in
  the README); `make release VERSION=<tag>` tags and pushes.
- **tie-filehost** reduced hash sharding from 4 levels to 2 and dropped
  thumbnail generation; it stays a pure content-addressed byte store.

### Fixed

- **tiedb write path hardened**: fixed a delete-loss race in the writer
  goroutine, replaced the capped `freespace` channel with an uncapped
  mutex-guarded slice (so the file no longer grows while holes exist), and added
  graceful shutdown that drains in-flight handlers before closing the DB.
- **Bulk-load memory** in the association index; memory-mode correctness, a
  triple cache, and sorted pagination.
- **Untrusted-filehost transfers** in the download path are now hardened.

### Removed

- The `tie-handle` command and its orphaned getlib driver.
- The `certmagic` dependency (filehost), the `gods` v1 dependency, and dead
  code across client/getlib.

[v0.4.3]: https://github.com/uidbz/tie/releases/tag/v0.4.3
[v0.4.0]: https://github.com/uidbz/tie/releases/tag/v0.4.0
