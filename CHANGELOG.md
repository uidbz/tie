# Changelog

All notable changes to this project are documented here. The format is loosely
based on [Keep a Changelog](https://keepachangelog.com/), and the project aims
to follow semantic versioning.

## [Unreleased]

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

[v0.4.0]: https://git.sr.ht/~uid/tie/refs/v0.4.0
