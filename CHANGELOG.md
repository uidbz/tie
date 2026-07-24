# Changelog

All notable changes to this project are documented here. The format is loosely
based on [Keep a Changelog](https://keepachangelog.com/), and the project aims
to follow semantic versioning.

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
