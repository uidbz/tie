# Changelog

All notable changes to this project are documented here. The format is loosely
based on [Keep a Changelog](https://keepachangelog.com/), and the project aims
to follow semantic versioning.

## [Unreleased]

### Added

- **`tie-smb`: the tie filesystem over SMB2/3.** A standalone server
  (`cmd/tie-smb`) exports the same live tag-derived tree as `tie mount --db`
  (`query/`, `files/`, `tags/`) as a single SMB share, pure Go via
  `github.com/go-filesystems/smb` (SMB 2.1/3.0/3.0.2, NTLMv2, signing) — no
  Samba. TOML config with `[[Users]]` read/write roles (same role model as
  tie-filehost), graceful SIGTERM shutdown, systemd unit with
  `CAP_NET_BIND_SERVICE`. Full write parity with the mount: create/write/
  rename/mkdir/delete under `files/`, tag and dir-type editing under
  `tags/`. tie names are escaped reversibly into SMB-legal display names
  (`~XX` hex for illegal bytes, trailing dot/space, reserved device names).
  See [docs/smb.md](docs/smb.md).
- **Transport-agnostic tree core (`io/tiefs`).** The query/path/meta/write/
  cache semantics of the `--db` mount moved out of `io/fuselib` (now thin
  adapters) so FUSE and SMB share one implementation. `io/smbfs` is the SMB
  adapter (`filesystem.Filesystem` + `Opener` + `WritableFile`).
- **Delete support in the client (`client.DeleteFile`/`client.DeleteDir`)**:
  unlink a file from one directory (shared content keeps its metadata),
  remove empty directories; wired to FUSE `Unlink`/`Rmdir` and to SMB.
- **Tests:** `io/smbfs` escaping/resolution unit tests, live-store tests,
  and an end-to-end SMB integration test using
  `github.com/hirochachacha/go-smb2` as a test-only client.

### Performance

- **`tie verify --check-blobs` runs 16 HEAD requests at a time.** It used to
  ask for one blob per round trip, so a library with a million files cost a
  million sequential requests — hours. With a small worker pool the same check
  takes minutes (measured: 400 HEADs at a 5 ms round trip, 2.07 s sequential
  vs 139 ms pooled). The filehost transports also keep 32 idle connections per
  host instead of the stdlib's 2, so the concurrent workers stop paying a fresh
  TCP handshake per request.

### Fixed

- **`--collection X` now means X everywhere.** The client is bound to one
  collection, and the extra `collection` argument that many methods took was
  honoured by only some of their internal calls: `tie import --collection X`
  wrote the file triples into X but created the directory nodes in the default
  collection, leaving parent edges pointing at DirUIDs X has never heard of
  (what `verify` reports as dangling parent references). `WriteFile` and
  `RepairOrphans` mixed them the same way. The argument also named a config
  entry loosely: `--collection music` wrote to a collection literally called
  "music" instead of the `[Collections.music]` entry's collection.
- **A mount rename can no longer move a directory inside itself.** `mv a a/b/x`
  over the FUSE mount or SMB succeeded and left the subtree unreachable from the
  root — the cycle `tie verify` reports. It is refused now (EINVAL on the mount),
  and a rename onto the same name is a no-op, as POSIX defines it.
- **Two concurrent `mkdir`s no longer mint two directories.** `MkTieDir` asked
  "is there a UID for this path?", saw none, minted one and wrote it — so two
  creators racing on one path (two imports, a FUSE `mkdir` against an import, an
  SMB client) both wrote, which is where the duplicate-path state `verify`
  reports comes from. There is now a `MkDir` request that does the lookup and
  the writes under one lock on the server and answers with the UID the path
  actually has. `MkTieDirAll` also walks top-down passing each level's UID to
  the next: one request per level instead of up to three round trips per level.
  "Already exists" is the sentinel `client.ErrDirExists` rather than an error
  string matched with `strings.HasSuffix`, and virtual `tie:/…` paths are split
  with `path`, not `filepath` (which would break on Windows).
- **A mount write that cannot be committed keeps its bytes.** `Close()` on a
  staged FUSE/SMB write committed and then always deleted the staging file, so
  if the filehost or triplestore was unreachable the user's just-written file
  was gone — silently, because most applications ignore the error from
  `close()`/`release()`. The bytes are now moved to a spool
  (`$TIE_SPOOL_DIR`, else `$XDG_STATE_HOME/tie/unsaved`) named after the
  intended path, logged, and named in the error.
- **One path from a collection to its filehost.** `ResolveHost(name)` and
  `ResolveHosts(collection, explicit)` answered the same question separately,
  and every caller hand-rolled the `[FileHosts]` lookup afterwards. The
  visible bug: `tie verify -c other --check-blobs` asked the *client's*
  collection's filehost, so every blob of the other collection was reported
  missing. `ResolveHostIn(collection, name)` is now the single path, used by
  upload, verify, repair and the CLI.
- **Mount inode numbers are derived from the key, not remembered.** The
  FUSE/SMB inode map grew by one entry per distinct key ever listed and was
  never pruned — a slow leak in a tie-smb server that runs for months — and
  because the numbers came from a per-process counter, the same file changed
  inode when the mount restarted. They are now 64-bit FNV-1a of the key:
  stable across restarts, no map, no lock. Collisions are possible and
  tolerable (two files would look like hard links); 0 is never handed out.
- **`tie verify --fix` refuses to repair when the index check reported
  problems in the same run.** The tree scan it repairs against is seeded by
  reverse queries, so with an inconsistent index it can contain phantoms or
  miss children — and the repairs are destructive. Fix the index first
  (`tie verify --index --repair`). A `--fix --dry-run` still prints its plan,
  since printing changes nothing.
- **Re-importing a library no longer re-sends it.** `putlib` now asks
  `HEAD /{hash}` before streaming a body and skips the upload when the filehost
  already holds that content address (the answer must also report the same
  length, so a 200 from something that is not a tie filehost is never taken as
  "stored"). Benchmark, re-importing an unchanged 20-file/640 KiB tree:
  1.5 ms / 3.67 MB → 0.8 ms / 0.96 MB in-process; over a network the body bytes
  are the difference.
- **A symlink to a directory no longer aborts a tree import.** It was
  classified by `Lstat` as a file, hashing failed with "is a directory", and the
  whole import stopped. Links are now followed, with the real paths of the
  directories on the current path tracked so a link back up the tree is reported
  as a loop instead of recursing forever. A dangling link is reported by name.

- **Filenames containing TAB, LF or CR no longer corrupt tie directory
  manifests.** Such a name is legal on Linux and used to be written raw into a
  TAB/LF-delimited manifest line, so the line failed to parse and every reader
  skipped it: the file disappeared from `download`/mount, and the reaper's
  `dirChildren` never saw the child, leaving a referenced blob unprotected.
  Names are now percent-encoded (`%`, TAB, LF, CR) in the filename column; the
  line structure and the `tiedir-v2` header are unchanged, and decoding is
  lenient so manifests written before this still read back. A name that cannot
  be represented at all (a backslash, a separator on Windows) is now refused at
  upload instead of being stored and silently dropped.

### Breaking

- **`client.TieClient` addresses one collection; `tc.In(name)` picks which.**
  The per-call `collection string` argument is gone from `Verify`, `CheckIndex`,
  `RepairTree`, `RepairOrphans`, `ImportDir`, `ImportFile`, `ImportAlbums`,
  `WriteFile`, `WriteFileWithProgress`, `ListVersions`, `RestoreVersion`,
  `RepairNames`, `Tag`, `ResolveHosts` and the internal helpers, and so is the
  whole `*In` family (`QueryIn`, `GetIn`, `ExpandIn`, `SyncIn`, `NewBatchIn`,
  `ResolveHostIn`): the argument was honoured by only some of a method's internal
  calls, which is what split one import across two collections. Scope a client
  instead — `tie.In("music").Verify(false)` — and it cannot mix.
- **The triplestore client no longer replays writes, and it has a timeout.**
  `webservice.Client.Run` retried *every* request after a transport error, on the
  grounds that writes are idempotent — but an `Update` whose response was lost has
  already applied, so the replay failed with "Did not find ..." and the client
  reported an error for a write that succeeded. Only reads and `Sync` are replayed
  now. There is also a 60 s per-request timeout (a stalled server used to hang the
  CLI, the FUSE mount and tie-smb forever; `SetTimeout` changes it, streams are
  exempt), `RunContext`/`RunStreamContext` accept a caller context, `NewClient`
  returns an error instead of `nil` for a bad URL, and a non-2xx answer becomes a
  `*StatusError` (`errors.Is` works with `ErrUnauthorized`/`ErrForbidden`) instead
  of a JSON syntax error.
- **One filehost HTTP client: `io/fhclient`.** Three places implemented
  "HEAD a blob" and two implemented a ranged GET, each deciding on its own what
  a status code means (verify treated a 403 as "missing", repair printed the
  short hash, the media rescan accepted 206 while getlib rejected anything but
  200). `fhclient.Client` now owns `Stat`, `Get`, `GetContent`, `GetRange`, `Put`
  and `Version`, validates the content address before it reaches a URL, and
  `io/putlib`, `io/getlib`, the tiefs cache producer and the FUSE content read
  are built on it. `putlib.UploadFile`/`UploadMultipart` take the filehost client
  and the content address instead of a full upload URL.
- **Server configs with an empty password or a duplicate username are refused
  at start-up.** `auth.BuildStore` validates `[[Users]]` for both servers: an
  empty (or whitespace-only) password made an account authenticatable by anyone
  who knew the name, and a duplicate username used to be silently overridden by
  the last entry. `resolve` also never matches a blank configured password, even
  for a Store built directly.
- **The triplestore answers failures with real HTTP status codes.** An
  application error used to be `200 OK` with `Success:false`, an unknown request
  id was `200` with an empty body, and a request body of any size was accepted.
  Now: application errors are 4xx/5xx (400 bad name or bad JSON, 404 unknown
  request or absent collection, 500 internal) with the same JSON `ReplyStatus`
  body, an oversized body is 413 (`MaxRequestBytesMB` in the config, default
  256 MiB), and `ErrorToJsonString`'s fallback is valid JSON. The Go client is
  unaffected (it decodes the body regardless of status); scripts that ignored
  `Success` and trusted the status will now see failures.
- `go.mod` now requires Go 1.27.1 (pulled in by the smb libraries' toolchain
  requirement).

## [v0.6.0] - 2026-10-12

### Added

- **Media collections are first-class (`client/media.go`).** A shared
  vocabulary for media collections (albums, playlists, picture sets, and —
  vocabulary only for now — movies and TV series/seasons/episodes) and the
  *collection contract*: every member carries `(member, member-of, C)` (so a
  collection lists in one reverse query whatever its folder depth — the
  triplestore indexes `member-of` in reverse by default; **restart the
  triplestore** for existing data), and `C` carries a stable `added-at`
  (first import; unlike `tag-date` never rewritten) and `member-count`.
  Audio albums also get an album summary (`album-artist`, top `genre`s,
  `track-count`, `disc-count`, total `duration`; `album`/`artist`/`year`
  when the root lacks them, as multi-disc roots do) and disc subfolders are
  labeled `(uid, tie-type, disc)` + `disc <n>`. `ImportDir` and
  `ImportAlbums` index the collection they create (`IndexCollection`);
  `ImportFile` stamps `added-at`. Media value types are declared
  (`EnsureMediaSchema`).
- **More per-track metadata at import:** `genre` (multi-valued, split by
  `SplitGenres`), `composer`, `disc`, `disc-total`, `track-total` and a
  `meta-version` marker. `metadata.Media` gains `Genre`, `Composer`,
  `TrackTotal`; `ExtractMediaMetadataFrom(io.ReadSeeker)` reads a stream.
- **Playlists (`client/playlist.go`):** `tie-type playlist` subjects with an
  ordered `playlist-track` list and `member-of` edges — no fake parent
  edges. `CreatePlaylist`, `ReadPlaylist`, `ListPlaylists`,
  `SetPlaylistTracks`, `RenamePlaylist`, `DeletePlaylist`, and
  `MigrateLegacyPlaylists` for tie-audio's old audio-dir playlists.
- **Play history (`client/history.go`):** play events as subjects
  (`play-of`, `play-in`, `played-at`, `listened`, `outcome`, …) with ids
  derived from (source, seq), so re-delivery is idempotent; `RecordPlays`
  keeps `play-count`/`skip-count`/`first-played`/`last-played`/
  `last-skipped` denormalized on items and collections, `RecentPlays`
  lists newest first (server-side sort), `RecomputeStats` rebuilds the
  counters, `ClassifyPlay` is the complete/skip rule (> 30 s long and half
  or 4 min heard = complete; < 30 s or < 25 % = skip).
- **CLI:** `tie media index|backfill [--rescan]|restat|schema`,
  `tie history [--since 30d] [--skips]`,
  `tie playlist list|show|create|delete|migrate`. `backfill` brings data
  imported before the collection contract up to it; `--rescan` re-reads
  audio tags (ranged head fetches) for fields older imports never wrote.

## [v0.5.3] - 2026-09-16

### Added

- **Disc-aware album placement (`cd1`/`cd2` subdirectories).**
  `PlanAlbumImport` now routes each album member to a disc-aware destination
  below the album root: a **multi-disc** album (more than one disc number
  among its tracks, or a disc-total tag above 1) places every disc'd file at
  `cd<N>/…`, and a **single-disc** album drops disc-like directory levels so
  it sits flat at its root. The disc number comes from the track's
  DISCNUMBER tag when present (`metadata.Media` gains `Disc`/`DiscTotal`,
  populated by `ExtractMediaMetadata` and the planner's probe), falling back
  to a disc-like directory name (`CD1`, `Disc 2`, `Album (Disc 2)`) — many
  libraries carry the structure only in their layout. Members with no disc
  signal keep their legacy relative placement, and albums with no disc
  structure at all import exactly as before. A dir group with a disc
  structure converts to an explicit import (`AlbumGroup.Files` +
  `Sidecars` + `SubPaths`) so faithful mirrors normalize too without losing
  cover art; colliding destination subpaths surface as plan warnings.
- **`client.ValidateDestTemplate`** reports whether an `[ImportDest]`
  template string is renderable (unknown or unterminated `{variables}`), so
  UIs can reject a bad template before scanning a library.

### Fixed

- **`Expand`/`Get` no longer return an empty row for a fully-deleted
  subject.** A subject's association set outlives its last triple, so
  `ExpandKeys` used to emit a row with no attributes, making a deleted node
  look like it still existed (client `Get` now returns `ErrNotFound`).
- **tiedb: forward and reverse indexes could silently diverge.** Creating a
  subject's (or a directory's) association set was a Get-then-Put on the outer
  index map, so two concurrent inserters for a brand-new key both created a
  set and the second overwrote the first — dropping its entry from one index
  while the other index, whose key already existed, kept it. The parallel
  load workers hit this on **every server start**: on a 2M-triple file about
  18% of forward entries were lost per load, surfacing as "phantom" subjects
  that reverse queries (`tie-type`, `tag`) returned but `dump` never showed
  and `Delete` could not clear, and as children missing from a directory's
  reverse `parent` lookup although their forward edge existed. Index sets are
  now created atomically (`lockedTree.GetOrPut`); `Add` runs its exists-check
  and insert under the change lock, so two concurrent adds of one triple no
  longer write two on-disk records (the second of which resurrected a deleted
  triple at the next load); every record a query resolves is validated
  against the index entry that pointed at it (a stale cache line or reused
  slot is evicted, re-read, and dropped rather than served as another
  subject's triple); the writer frees a slot only after its tombstone is on
  disk; and `Delete` removes a reverse-only residue and succeeds, so a phantom
  can be cleared with an ordinary delete. `tiedb/indexconsistency_test.go`
  reproduces the load and concurrent-add races and pins the fixes. Load speed
  is unchanged; the heap grows only by the entries that were previously lost.

### Added

- **Server-side index check and repair: `CheckIndex` / `tie verify --index`.**
  `tiedb.Collection.CheckIndex` walks the forward and reverse indexes and
  reports every divergence (missing-reverse, reverse-only, position-mismatch,
  and with `Deep` misresolved positions whose on-disk record no longer
  matches); `Repair` fixes them in memory — no restart needed. Exposed as the
  `CheckIndex` request (write role: it blocks writers for the walk), wrapped by
  `TieClient.CheckIndex` (Go) and `check_index` (Python). Bare `tie verify`
  now runs the index check **before** the tree scan — the scan is seeded by
  reverse queries, so an inconsistent index made it report phantoms and miss
  children — and `--repair` fixes the index first so orphan re-homing never
  acts on a phantom. `--index` runs the index check alone; `--deep` validates
  every index position against its record (slow on a large collection).
- **`tie verify --fix`: the destructive fsck repairs, folded in from
  `scripts/repair`.** `client.RepairTree` plans (and with `Apply` executes)
  what `--repair` never touches: dangling parent refs are re-parented to the
  nearest live ancestor, ghost nodes (dir-typed, no path, no children, no
  tiedir-hash referrer, no blob) are deleted, files get missing
  size/media-type/tie-type/filename re-derived from their blob, and nameless
  leftovers or files whose blob is gone are deleted. `--fix` implies
  `--repair`; `--fix --dry-run` prints the plan and changes nothing; every
  applied mutation is journaled as TSV (`--journal`). The standalone
  `scripts/repair` tool is removed. Covered end to end by `TestRepairTree`.
- **Version reporting.** `tie version` prints the client build and the builds
  of the configured triplestore (new read-role `Version` request) and
  filehosts (`GET /-/version`); both servers log version, commit and
  toolchain at startup. `version.Get()` falls back to the module version or
  VCS commit Go embeds in build info, so a plain `go build` no longer reports
  `dev`.
- **Docs:** `docs/verify.md` gains the index-check section and documents
  `tie restore --drop` as the field remedy for a collection whose on-disk
  state is suspect; `docs/internals.md` now describes the current writer
  (producer-side slot allocation, tombstone-then-free, 10s sync ticker) and
  the index invariants.

## [v0.5.2] - 2026-09-13

### Added

- **Bulk album import: `tie import <dir-type> --albums <root>`.** A whole
  library root can now be imported in one run. The new planner
  (`client.PlanAlbumImport`) scans the tree — one open per file on a wide
  worker pool, so network-mounted libraries scan in minutes — clusters it into
  albums, renders each album's destination, and prints a review table with
  per-album warnings (`--dry-run`); `client.ImportAlbums` then imports group
  by group, stamping the dir-type on every album root. Grouping is selected
  with `--by`: `auto` (default) treats one audio-bearing directory as one
  album but splits directories whose files carry conflicting album tags and
  merges directories sharing one album identity (multi-disc sets, scattered
  rips); `dir` is strictly one directory = one album; `tags` clusters purely
  by album tags. Whole-directory groups import as faithful tree mirrors via
  the existing `ImportDir` (sidecars included, full reconcile semantics);
  merged/split/tag-clustered groups import their audio files only,
  preserving disc subdirectories, and are additive-only. Audio-member zips
  become single-file `audio-archive` groups (only zip members are peeked —
  rar/7z/iso listings would stream the whole archive). Warnings flag missing
  tags, mixed artists with no album-artist tag, destination collisions, and
  groups nested inside a whole-tree import.
- **`{albumartist}` destination-template variable and `album-artist` file
  triple.** `metadata.Media` gains `AlbumArtist` (the tag fork already read
  it); templates render the aggregated album artist, falling back to the
  artist, so compilations land under e.g. "Various Artists" instead of under
  whichever track artist dominates. The value is also written per file at
  import time alongside title/artist/album/year/track.
- **Per-collection value-type registry.** An advisory `value-types` registry
  entity lets a client declare how to read the values under a relation (int,
  float, bool, date, datetime, string is the default). Values stay plain
  strings; the registry is a hint for readers, never enforced on writes.
- **`TieClient.ResolveHosts(collection, explicit)`** — collection-aware
  filehost resolution for import: a collection's own `FileHosts` are honored
  instead of always falling back to the top-level `DefaultFileHosts`.

### Fixed

- **Import no longer aborts with bare EOF on empty files.** `GetTieType` read
  its 261-byte sniff window with a single `Read`, so a zero-byte file returned
  `io.EOF` unwrapped and aborted the whole import; the sniff now treats
  EOF/ErrUnexpectedEOF on a short read as "classify what exists".

## [v0.5.1] - 2026-09-03

### Added

- **Multi-row (hierarchical) table headers.** `InsertTableLevels`/`ReadTableLevels`
  store and return a table's header as row-major header rows, so a two-row header
  (`Temperature (20°C)` over `Replicate 1 | Replicate 2`) survives storage instead
  of having to be flattened by hand. A header keys every cell triple in its column,
  so the cell model is unchanged: the levels ride in one new `column-levels`
  relation, and each column's key is its non-empty levels joined by `\x1f`. The
  change is purely additive — a single-row header is stored exactly as
  `InsertTable` stores it, `ReadTable` still returns the flat keys, and a table
  written before the feature reads back as depth 1 — so no migration is needed.
  Header rows must be rectangular with merged parent cells already forward-filled;
  deciding where a merged cell ends is file-parsing work that belongs to the
  caller, not to storage. Mirrored in the Python client (`insert_table` accepts
  either form, plus `read_table_levels`), which gains its first table tests,
  including cross-client round trips against the Go client.
- **`ReadTableFull`** returns a table's column keys, its header rows, and its rows
  together in one round trip, for callers that need both header views at once — a
  GUI keys each cell by its column key while rendering the levels above it, and
  the two narrower readers can only serve that by reading the table twice.

### Fixed

- **The Go client tests and the Python e2e cross-client checks both ran against
  nothing.** `client.TestingConfig()` pointed at `:1161`/`:1162` (the systemd
  defaults) while `test-env` deliberately runs on `:2161`/`:2162`, so
  `requireServer` skipped every client test unless a *production* triplestore
  happened to be listening. In `test_e2e.py` the Go CLI was invoked with
  `-c config.toml`, but v0.5.0 swapped the flags (`-c` is `--collection`,
  `-C` is `--config`), so it resolved a collection named "config.toml", printed
  nothing, and the cross-client assertions compared against empty output.

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
