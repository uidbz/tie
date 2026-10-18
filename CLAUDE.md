# tie

`tie` is a client for a triple-store (`tie-triplestore`) plus a content-addressed
blob store (`tie-filehost`). The CLI uploads/downloads files, tags them, and
mounts collections as a FUSE filesystem.

## Layout

| Path                 | What |
|----------------------|------|
| `cmd/tie/`           | CLI. One file per command group (`cmd_triple.go`, `cmd_tag.go`, `cmd_verify.go`, `cmd_repair.go`, …); `app.go` assembles the command tree, `format.go` the `--format` output, `progress.go` the only TTY-gated progress sink. |
| `cmd/tie-triplestore/` | Triple-store server. TOML-configured. |
| `cmd/tie-filehost/`  | Content-addressed blob store. TOML-configured. `reaper.go` = the expired-blob sweep. |
| `client/`            | `TieClient` — the Go API the CLI and the mounts call. `verify.go` = the `tie verify` consistency check, `pipeline.go` = the `tie repair` plan/journal codecs. |
| `webservice/`, `api/` | The HTTP protocol: `webservice` is transport + a typed handler registry (`Register[Req,Rep]`), `api` is the tie request types and `api.RegisterAll(ws)`. |
| `io/fuselib/`        | FUSE protocol layer. `fuselib.go` = content-addressed mount; `fuselib_db.go` = live tag-derived mount (`--db`). |
| `io/tiefs/`          | The virtual filesystem both mounts share: inode/tree (`tree.go`), **the blob read cache (`cache.go`)**, path parsing, writes. |
| `io/smbfs/`          | SMB server over the same `io/tiefs` layer. |
| `io/putlib/`, `io/getlib/` | Upload / download plumbing. `io/fhclient` is the filehost client, `io/archivelib` reads inside archives. |
| `internal/diskcache/` | The size-bounded, single-flight, refcounted **disk cache engine** — shared by the `io/tiefs` blob cache and the filehost read cache. |
| `tiedb/`             | The triple-store engine (association index; memory-sensitive — see `docs/memory-optimization.md`). |
| `metadata/`          | Blob metadata headers, the content address (`tiedir.go`), media tags. |
| `internal/testenv/`  | In-process triplestore + filehost for tests. |
| `test-env/`          | Local end-to-end sandbox. Start here to run anything by hand. |

## Running the stack (test-env)

Everything is localhost, plaintext HTTP. Triplestore `:2161`, filehost `:2162` —
deliberately off the systemd defaults (`:1161`/`:1162`) so the sandbox can run
beside a live system stack. `client.TestingConfig()` and the Python tests point
here; code that hardcodes 1161 talks to the wrong store.

The Go client and API suites do **not** need this sandbox: `client`'s `TestMain`
starts a real triplestore and filehost inside the test binary
(`internal/testenv`), so the suite never skips and never depends on `start.sh`.
Set `TIE_TEST_WEBSERVICE`/`TIE_TEST_FILEHOST` to run it against external servers
instead.

```bash
cd test-env
./build.sh        # go build all three binaries into bin/
./start.sh        # start triplestore + filehost, generate their TOML configs if absent
./seed.sh         # upload + tag sample files
./mount-db.sh     # mount live tag tree at mnt/ (foreground, Ctrl-C to unmount)
./stop.sh         # unmount + kill both services
```

- `env.sh` holds all shared paths/ports and the `tie_cli` helper. Source it, then
  use `tie_cli <args>` (it `cd`s into test-env and passes `-C config.toml`).
- **Both servers are TOML-configured** (`-config <file>`), not flags. `start.sh`
  generates `tie-triplestore.toml` and `tie-filehost.toml` if missing. Both are
  gitignored runtime artifacts; only `config.toml` (the CLI config) is tracked.
- Filehost config keys: `ListenOn`, `Insecure`, `BlobPath`, `BlobPaths`,
  `CertFile`/`KeyFile`, `ReapInterval` (Go duration; `"0"` disables the
  expired-blob reaper), `ReapDryRun` (log what a pass would remove, delete
  nothing), `LogFile`/`LogLevel`, `CachePath`/`CacheSizeGB` (the read cache),
  `[[Users]]` and `AnonymousAccess` — see access control below.
- **Multi-store filehost.** `[[BlobPaths]]` (`Name`, `Path`, `DefaultRetention`)
  defines one or more physical stores in a single filehost process; `BlobPath`
  is the deprecated single-store form (an empty `BlobPaths` synthesizes one
  `"default"` store from it, permanent retention). Uploads route to a store by
  the `Tie-Store` request header (empty → the `"default"` store, else the first
  listed); reads (`GET`/`HEAD /{hash}`) stay hash-only and `findBlob` searches
  every store. Dedup is **per-store** — each store keeps its own
  `.tie-retention.json`. A store's `DefaultRetention` (Go duration or
  `"infinite"`/empty) is applied to uploads to it that carry no `Tie-Retention`
  header. The reaper computes protection **globally**: a directory blob in one
  store shields its children even when they live in another store
  (`dirChildren` resolves via `findBlob`), then sweeps expired-and-unprotected
  blobs per store. Client side: `FileHost.Store` rides uploads as `Tie-Store`,
  so two named `[FileHosts.*]` entries can share a URL but target different
  stores.
- Triplestore config keys: `ListenOn`, `Insecure`, `DbPath`, `CertFile`/`KeyFile`,
  `LogFile`/`LogLevel`, `MaxConcurrentRequests` (0 = unbounded),
  `LoadMemoryLimitMB`, `MaxRequestBytesMB` (a larger body is answered 413),
  `[[Users]]` (Username/Password/Role), `AnonymousAccess`, `ReverseRelations`,
  and `[[Collections]]` overrides — see below.
- **Access control (both servers).** HTTP Basic Auth with a shared role model in
  the `auth/` package: roles are `none < read < write` (write ⊇ read). Each
  request is classified read or write and allowed iff the caller's role covers
  it. `[[Users]]` gained a `Role` field (`"read"` or `"write"`; **empty defaults
  to `"write"`** so pre-existing users keep full access). `AnonymousAccess`
  (`"none"`|`"read"`|`"write"`) is the role granted to a request with no valid
  credentials. **Filehost defaults `AnonymousAccess = "write"`** (fully open —
  preserves unauthenticated uploads; opt in by setting `"read"` to lock uploads
  or `"none"` to lock everything). **Triplestore defaults `"none"`** (unchanged
  always-authenticated behavior). Passwords are plaintext in config; only the
  wire compare is constant-time (`subtle.ConstantTimeCompare`) — hashing is a
  deliberate non-goal for now. **The read/write classification is part of
  registering the request** (`webservice.Register(ws, id, access, handler)`; the
  tie protocol lists them in `api.RegisterAll`, and `ws.AccessFor(id)` reports
  what a request needs). Read requests: `Dummy`, `Query`, `Expand`, `Associated`,
  `CoTags`, `Version`, `Dump`; every other Id (incl. new ones, and `CheckIndex`
  even without repair — it blocks writers) is write, and an id nobody registered
  defaults to write too (fail-safe). `api/register_test.go` fails if a request is
  registered without appearing there. 401 = missing/failed auth; 403 = valid
  user, insufficient role.
  Client filehost creds live in `[FileHosts.<name>]` (`Username`/`Password`) and
  ride every request via a Basic-Auth `http.RoundTripper` in
  `client.HTTPClientFor`.
- **Client config (`client.Config`).** The triplestore URL key is `TripleStoreURL`
  (the older `Webservice` key was honored through v0.6.0 and is now refused with
  a message naming the replacement). One config can bind several collections via
  `[Collections.<name>]` entries (each may override `Namespace`, `Collection`,
  `TripleStoreURL`, `Username`/`Password`, `Insecure`, `FileHosts`; unset fields fall
  back to the top-level values). `DefaultCollection` picks the one used when no
  collection is named. A config with **no `[Collections]` is deliberately left
  without one**: an entry synthesized from the flat `Namespace`/`Collection`/
  `DefaultFileHosts` fields would shadow them, and an embedder that loads a
  config and then overrides `Config.Namespace`/`Config.Collection` (the
  documented way to target a collection programmatically) would be silently
  ignored. `Config.ResolveCollection`'s unnamed path resolves the flat fields
  identically, so nothing is lost: it turns a name (or `""`) into the concrete
  `ResolvedCollection`; `NewTieClientFor`
  binds a client to it, and `tc.In(name)` returns a client scoped to another
  collection (sharing the transport). Host selection for file ops is
  collection-aware: `TieClient.ResolveHosts(explicit)` returns the `--host` list
  when given, else the resolved `FileHosts` of **the client's bound collection**
  (global `-c`, or whatever `In()` scoped it) — where a collection's own
  `FileHosts` override top-level `DefaultFileHosts`. `import` mirrors to every
  resolved host.
  **CLI flag change: `-c`/`--collection` selects the
  collection; the config file is now `-C`/`--config`** (they were swapped). The
  global `-c`/`-C` are peeked from `os.Args` before cli parsing in
  `parseGlobalFlags` (the command tree is built from config first).
- **Reverse-relation config.** `ReverseRelations` sets which relations (value1)
  every collection indexes in reverse; omit it for the built-in default
  (`tag`, `path`, `parent`, `tie-type`, `version-of`, `member-of`). `member-of`
  lists media collection members (client/media.go). `version-of` powers
  `tie versions list` in the `<Collection>_prev` history collection and costs
  nothing on collections holding no version records. Add a `[[Collections]]` block
  (`Namespace`, `Collection`, `ReverseRelations`) to override the set for one
  collection. The reverse index is rebuilt from forward triples at collection
  load time, so a change needs a **triplestore restart** to take effect for existing
  data. Trimming the set cuts association memory on metadata-heavy stores; widen
  it per-collection only for the relations a client actually queries in reverse.
- The triplestore's DB load can be slow on a large `db/`, so `start.sh`'s readiness
  loop may run for a while before both ports answer — that's expected.

## CLI commands

`add`/`del`/`get` (triples), `upload`/`download` (files), `import` (dir trees),
`stat`, `mount` (FUSE), `dump`/`restore` (TSV backup), `verify`/`repair` (fsck),
`tag`, `versions`, `conf`, `media`, `playlist`, `history`, `version`,
`completion`. There is no `put` — use `upload`.
`tie upload <file>` prints the content hash. `mount --db <mnt>` for the live tag
tree; `mount <hash> <mnt>` for an immutable content-addressed dir.

Anything that answers rows (`get`, `tag files`, `verify`, `repair plan`,
`conf show`, `stat`, `dump`) writes TSV on stdout and switches to JSON with
`--format json`; stdout stays data (diagnostics and progress go to stderr, and
progress only when stderr is a TTY — `cmd/tie/progress.go`). Exit codes: **0**
clean, **1** findings/problems, **2** usage, **4** operational (server
unreachable, I/O). Errors read `tie <command>: what went wrong`, lower case, no
nested `Error:`.

`get` takes several keys (their rows intersect) and drops rows with `--not VALUE`
or the `!VALUE` spelling; `del` chooses its shape with flags (`--relation`,
`--all`) rather than wildcard arguments; `get`, `stat`, `download` and `tag set`
read keys from stdin with `-` or `--stdin` — `--stdin` is the spelling that
survives arguments written after it, because a lone `-` stops the argument
parser.

`import <dir-type> --albums <root>` bulk-imports a whole library
(`client/albums.go`): `PlanAlbumImport` scans the tree (wide worker pool +
single-open probing — libraries live on network mounts; zip-only member
peeking, since rar/7z listings stream the whole archive), clusters it into
albums (`GroupAuto` = per-directory, split on conflicting album tags, merged
on shared album+albumartist identity; `GroupDir`/`GroupTags` are the literal
extremes), renders per-album destinations from the dir-type's `ImportDest`
template (`--dest` doubles as an inline template; `{albumartist}` falls back
to artist), and annotates warnings (missing tags, dest collisions, nesting).
`ImportAlbums` executes: whole-tree groups via `ImportDir`, file-list groups
via batched per-file imports that are additive-only (no reconcile — a partial
view must not version away siblings). CLI has `--dry-run` and `-y`.

Placement is disc-aware: multi-disc albums route each disc'd member to
`cd<N>/…` and single-disc albums drop disc-like directory levels, keyed off
`metadata.Media.Disc`/`DiscTotal` (DISCNUMBER tag) with a fallback to
disc-like directory names (`CD1`, `Disc 2`) for libraries that carry the
structure only in their layout. The planner writes per-file destinations
into `AlbumGroup.SubPaths` (nil = legacy verbatim placement); a
disc-structured dir group converts to an explicit import
(`Files`+`Sidecars`+`SubPaths`, cover art rides along) instead of an
`ImportDir` mirror. `client.ValidateDestTemplate` checks a template string
before scanning.

`verify` is the store's fsck (see docs/verify.md). Bare `tie verify` first
runs the server-side **index check** (`CheckIndex` request →
`tiedb.Collection.CheckIndex`), then a read-only scan of the whole collection
for lost or incomplete nodes: orphaned dirs/files (no `parent` edge —
unreachable from the root), dangling parent references, parent cycles,
duplicate path claims, and files missing core metadata; it exits non-zero if
anything is found, so it is cron-able. A `tiedir` snapshot blob carries
`tie-type: directory` but no `path`, so verify partitions on the `path` triple
and checks such blobs as files, never as orphaned dirs. `verify --repair`
repairs the index in memory, then re-homes only the orphans under
`tie:/restored/<date>/` (one added parent edge each; metadata untouched) and
re-scans so the exit code is post-repair — every other problem class is reported
but never auto-fixed. `verify --index` runs the index check alone; `--deep`
also resolves every index position against its on-disk record (slow, blocks
writers). `verify --check-blobs` additionally confirms each file's content
exists on the filehost via a cheap `HEAD /{hash}` stat endpoint (200/404, no
body, no cache copy). `verify --fix` (implies `--repair`; `--dry-run` prints
the plan, every applied mutation is journaled as TSV via `--journal`) applies
the destructive repairs `client.RepairTree` plans: dangling parent refs are
re-parented to the nearest live ancestor, ghost nodes (dir-typed, no path, no
children, no tiedir-hash referrer, no blob on the filehost) are deleted, and
files get missing metadata re-derived from their blob (size via HEAD, type by
sniffing, filename reconstructed from name+extension). Nameless leftovers and
files whose blob is gone are deleted (have a `dump` backup first). Cycles and
duplicate paths are only ever reported.

`tie repair` is the same fsck split into its two halves, so either side can be
piped or reviewed by hand: `repair plan` reads the findings `tie verify` writes
and answers the triple operations that would fix them; `repair apply` reads those
operations, runs them, and echoes the journal. A journal is a plan with a
timestamp in front, so a journal can be replayed with `apply`; `verify --fix
--journal FILE` is the same pipeline in one command.

`tie version` prints the client build and the builds of the configured
triplestore (`Version` request, read role) and filehosts (`GET /-/version`).
`version.Get()` prefers the Makefile's ldflags (`git describe`), else the
module version / VCS commit Go embeds in build info, so a plain `go build`
still reports the commit; both servers log it at startup.

**Forward/reverse index consistency (tiedb).** The forward index is
authoritative (the `.tie` file holds forward records only); the reverse index
is derived. Divergence — reverse-only *phantoms* (subjects reverse queries
return but `dump` never shows, which `Delete` could not clear) and forward
`parent` edges missing from the directory's reverse lookup — came from a
lost-update race in `putAssoc` (Get-then-Put of a new `AssociationSet`), hit by
concurrent `Add`s **and by the 8-worker load on every restart** (on a 2M-triple
file, ~18% of forward entries were lost per load). Fixed by
`lockedTree.GetOrPut`; `Add` now runs exists-check + insert under
`changeMutex` (no duplicate on-disk records); `resolveEntry` validates each
resolved record against its index entry (stale cache line / reused slot →
evict, re-read, drop); `Delete` clears a reverse-only residue and returns ok.
`tiedb/indexconsistency_test.go` reproduces the races and pins the fixes;
`tie verify --repair` (or `--index --repair`) fixes a live server's divergence
in memory without a restart; `tie restore --drop` is the field remedy when the
on-disk state itself is suspect.

`completion <bash|zsh|fish|pwsh>` prints a shell-completion script (the
urfave/cli built-in, un-hidden in `cmd/tie/main.go` via
`ConfigureShellCompletionCommand`); enable it with e.g.
`source <(tie completion bash)` in `.bashrc`.

`tag` groups tag management: `list` (registry names), `add <tag>` (register a
name only), `del <tag>` / `rename <old> <new>` (rewrite the tag on every item +
the `(tags,"all",…)` registry, globally), `files <tag>` (items carrying a tag),
`show <hash>` / `set <hash> [tags…]` (per-item tags), and `untagged
[--type <tie-type>]` (items with a tie-type but no tag). Tags attach to a
content hash, so `del`/`rename`/`set` change the tag everywhere that content
appears. `untagged` is backed by the server-side `MissingRelation` query
predicate (see below), so only untagged rows cross the wire.

## Query: MissingRelation ("has no X") predicate

The association algebra's `Exclude` removes a *specific value*; it cannot express
"lacks any triple under relation R". `QuerySpec.MissingRelation` (→
`api` `missingRelation` → `tiedb.TagQuery.MissingRelation`) fills that gap: after
the normal Include/Exclude/Scope resolution, `Collection.filterMissingRelation`
keeps only seed subjects whose *forward* index has no entry under R (via
`AssociationSet.HasRelation`, an early-exit scan over a subject's small metadata
set). Cost is O(candidates) forward point-lookups, memory O(result) — no
full-collection scan or client-side download, so it holds to the memory-over-load
weighting. `UntaggedFiles`/`tag untagged` set `MissingRelation="tag"` scoped to a
`tie-type`; any client (e.g. imgview) can pass the field on a normal `Query`.

## Read path: the blob cache (perf-relevant)

`mount` → `NewTieDBFuse`/`NewTieFuse` with a `--cache` size in GB (default 1);
the SMB server reads through the same layer. The cache is `io/tiefs/cache.go`,
and its engine is `internal/diskcache` — the same size-bounded, single-flight,
refcounted disk cache the filehost's own read cache uses, so both share one set
of semantics rather than two near-copies.

- **Disk-backed and single-flight.** The first read of a hash `GET`s the blob
  once, streams it to a file under a temp dir (`io.Copy` — memory is bounded by
  the copy buffer, not the file size), then serves reads by `ReadAt` on the open
  fd. Concurrent readers of the same hash coalesce on a per-entry `ready`
  channel: one download regardless of reader count.
- **Handles are refcounted.** `Cache.Blob` returns a `*tiefs.Blob` holding a
  reference on the entry. Eviction unlinks the name but closes the fd only when
  the last handle closes, so an evicted blob stays readable until then (the
  §3.2 EIO). FUSE re-resolves per read and releases immediately, which keeps the
  entry hot in the LRU instead of pinning a multi-GB blob for the whole life of
  an open file; SMB holds one per open handle.
- **The budget is a soft LRU target, not a hard limit.** Eviction is true LRU
  (`container/list`, move-to-back on a hit — not insertion order), and a file
  larger than the budget still reads fine: eviction simply cannot make room for
  anything else until it goes.
- `TieFuse.Close()` / `TieDBFuse.Close()` remove the temp dir and `mount` defers
  it, so a normal unmount cleans up (a `kill -9` can't run the defer and leaves a
  `/tmp/tie-cache-*` dir).

The filehost answers a blob with `http.ServeContent`, streaming from the sharded
store (`data/<xx>/<yy>/<hash>`) — or through its read cache's own handle, so an
eviction mid-request cannot turn a download into a 404. Copying the whole blob
before the first byte is what used to put minutes between a request and its
first byte.

**Byte verification is off by default.** Reads do *not* hash downloaded bytes
against their content address — for a trusted personal filehost that hash pass
over multi-GB media is wasted work. Pass `mount --verify` to turn it on (streams
through a hashing `MultiWriter` and rejects a mismatch); use it when the filehost
is untrusted. The flag threads through `NewTieFuse(..., verify bool)` to both the
blob `download` and the directory-blob `listContent`.

## DB write path & durability (tiedb)

In disk mode each `Collection` runs one **writer goroutine** (`dBWriter`) that
owns the `.tie` file handle for its whole lifetime. All writes and disk reads go
through it via `dBWriteQueue`/`dBReadQueue`; producers never touch the fd.

- **Slot allocation happens in the producer, not the writer.** `Add`/`insertValue`/
  `insertHash` call `allocSlot()` to reserve the on-disk offset up front (a
  reclaimed `freespace` slot, else the `db_size` append cursor), insert into the
  in-memory tree at that real position, and pass the offset to the writer as
  `FileMod.Position`. The writer only persists bytes — it must **never** re-insert
  into the tree (doing so raced a concurrent `Delete` and resurrected deleted
  triples).
- **`freespace` is a mutex-guarded `[]int64`** (mirrors `arenaFree` in memory
  mode), not a channel, and has **no cap** — every freed slot is reclaimable, so
  the file does not grow while holes exist. Slots are freed by the writer *after*
  the tombstone is written, so a reused slot is never handed out before its old
  contents are overwritten.
- **`db_size` has a single owner:** `initDBSize()` sets it once before the writer
  starts (trimming any partial trailing record from a crash). `openDB` must not
  reset it — `allocSlot` is the only mutator thereafter.
- **Durability = a 10s `time.Ticker` `Sync()`** inside the writer loop (not an
  idle-close; holding the fd open is free). `closeDB` signals the writer to drain
  queued writes, `Sync()`, close, and exit — `TieTree.Close()` fans this across
  all live collections.
- **Graceful shutdown:** the triplestore's `ListenAndServe` traps SIGINT/SIGTERM,
  `srv.Shutdown()`s the HTTP server (so in-flight handlers finish enqueuing their
  writes) **then** `ws.Close()` → `TieTree.Close()`. Ordering matters: draining
  handlers before closing the DB guarantees no acknowledged write is lost.
  `kill -9` can't run this — that tail falls back to the ticker + kernel flush.

## Conventions

- tiedb diagnostic/log output goes to **stderr**, never stdout (stdout is data,
  e.g. `dump` TSV).
- For tiedb changes, memory footprint is weighted ≥ load speed; benchmark old vs
  new under the same conditions before keeping a perf change.
- filehost stays a pure content-addressed byte store — no media/format parsing.
- **Stored and wire formats are stable.** The `.tie` record layout, the TSV
  `dump`/`restore`/plan/journal shapes, and the request/reply JSON do not change
  without a CHANGELOG entry saying so. When a change is intentional, a test pins
  the old bytes or the old string (e.g. `client/relations_test.go` pins every
  relation constant to the string stored on disk).
- **Result shapes are flat.** tiedb answers `[]StringTriple` (ordered page +
  total) or `[]Row` (subject + attributes); `Get` answers `[]string` for one
  relation. There are no nested-map results — the old `TripleSet` cost a map
  allocation per value and made JSON replies order-nondeterministic.
- **Relations are typed string constants** (`client.Relation`, e.g.
  `const RelTag Relation = "tag"`) — one vocabulary, no enum plus stringer. Wire
  and storage stay plain strings.
- **A new protocol request is a struct plus a handler**, registered with
  `webservice.Register[Req, Rep](ws, id, access, handler)`; the access level is
  part of registering, and `api/register_test.go` fails if the request is not in
  the protocol list.

## Filing issues, PRs and release notes (privacy)

The tracker is public and read outside this project. Anything written to GitHub —
issue titles and bodies, PR descriptions, comments, release notes, commit
messages — is written in **generalized form**, never with data from anyone's real
store:

- No real paths (`/mnt/…`, `/data/…`, `/home/…`), no hostnames or addresses from
  a LAN, no collection, namespace, directory or file names. Use placeholders:
  `tie:/a/b/c`, `/path/to/cache`, `<filehost>:1162`.
- Describe the *shape* of the state, not its content: "a collection where
  directories with a `path` triple sit under a parent that is nowhere near that
  path", not the 81 folders that do it here.
- Counts and identifiers from a live store are out unless they are essential,
  and then they are rounded (`thousands of entries`, `~1 kB manifest`).
- Screenshots, log excerpts and dumps attached to an issue must be scrubbed of
  names first; a synthetic reproduction is always preferable to an anonymized
  real one.

## Media model (client/media.go, playlist.go, history.go)

Shared media vocabulary + the **collection contract**: members point at
their collection with `member-of` (reverse-indexed by default — distinct from
the tree's `parent`, so multi-disc albums under `cd<N>/` list in one query);
collections carry a write-once `added-at` (RFC3339 UTC, `MediaTimeFormat`,
fixed width so string sort = chronological) and `member-count`; audio albums
an album summary (album-artist, genre, track-count, disc-count, duration).
`IndexCollection` (idempotent; Ops==0 when current; drops stale member-of;
skips nested collections and `_prev` dirs) runs at the end of
`ImportDir`/`importAlbumFiles` and tie-fm's `LabelDir`; `Backfill` /
`tie media backfill` for old data; `RescanAudio` (`--rescan`) re-reads tags
via ranged GETs for tracks below `meta-version` 2. Playlists are
`tie-type playlist` subjects (ordered `playlist-track` + member-of; no
`directory` marker — verify only enumerates directory/file typed subjects,
so playlists and play events are invisible to it). Play events
(`tie-type play-event`, id = sha256(source, seq)) with denormalized stats
(`play-count`, `last-played`, …) on items and collections; `RecordPlays` is
idempotent (Expand of the ids first). `metadata.Media` must stay comparable
(tie-fm compares it to the zero value) — hence `Genre string`, split at
write time.
