# tie

`tie` is a client for a triple-store (`tie-triplestore`) plus a content-addressed
blob store (`tie-filehost`). The CLI uploads/downloads files, tags them, and
mounts collections as a FUSE filesystem.

## Layout

| Path                 | What |
|----------------------|------|
| `cmd/tie/`           | CLI (`commands.go` has every subcommand). |
| `cmd/tie-triplestore/` | Triple-store server. TOML-configured. |
| `cmd/tie-filehost/`  | Content-addressed blob store. TOML-configured. |
| `client/`            | `TieClient` — the Go API the CLI and fuselib call. `verify.go` = the `tie verify` consistency check. |
| `io/fuselib/`        | FUSE. `fuselib.go` = content-addressed mount; `fuselib_db.go` = live tag-derived mount (`--db`). |
| `io/putlib/`, `io/getlib/` | Upload / download plumbing. |
| `tiedb/`             | The triple-store engine (association index; memory-sensitive — see auto-memory). |
| `test-env/`          | Local end-to-end sandbox. Start here to run anything. |

## Running the stack (test-env)

Everything is localhost, plaintext HTTP. Triplestore `:2161`, filehost `:2162` —
deliberately off the systemd defaults (`:1161`/`:1162`) so the sandbox can run
beside a live system stack. `client.TestingConfig()` and the Python tests point
here; code that hardcodes 1161 talks to the wrong store, or (via `requireServer`)
silently skips the whole Go client suite.

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
- Filehost config keys: `ListenOn`, `Insecure`, `BlobPath`, `CertFile`/`KeyFile`,
  `ReapInterval` (Go duration; `"0"` disables the expired-blob reaper),
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
  `MaxConcurrentRequests` (0 = unbounded), `[[Users]]` (Username/Password/Role),
  `AnonymousAccess`, `ReverseRelations`, and `[[Collections]]` overrides — see below.
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
  deliberate non-goal for now. Triplestore read requests: `Dummy`, `Query`, `Expand`,
  `Associated`, `CoTags`, `Dump`; every other Id (incl. new ones, and
  `CheckIndex` even without repair — it blocks writers) is write
  (fail-safe). 401 = missing/failed auth; 403 = valid user, insufficient role.
  Client filehost creds live in `[FileHosts.<name>]` (`Username`/`Password`) and
  ride every request via a Basic-Auth `http.RoundTripper` in
  `client.HTTPClientFor`.
- **Client config (`client.Config`).** The triplestore URL key is `TripleStoreURL`
  (`Webservice` is the deprecated alias; `LoadConfig`→`normalizeConfig` copies
  one into the other). One config can bind several collections via
  `[Collections.<name>]` entries (each may override `Namespace`, `Collection`,
  `TripleStoreURL`, `Username`/`Password`, `Insecure`, `FileHosts`; unset fields fall
  back to the top-level values). `DefaultCollection` picks the one used when no
  collection is named. A config with no `[Collections]` is normalized into a
  single synthesized entry from the flat `Namespace`/`Collection`/
  `DefaultFileHosts` fields, so old configs keep working. `Config.ResolveCollection`
  turns a name (or `""`) into the concrete `ResolvedCollection`; `NewTieClientFor`
  binds a client to it. Host selection for file ops is collection-aware:
  `TieClient.ResolveHosts(collection, explicit)` returns the `--host` list when
  given, else the named collection's resolved `FileHosts` — the client's bound
  collection (global `-c`) when `collection` is empty — where a collection's own
  `FileHosts` override top-level `DefaultFileHosts`. `import` mirrors to every
  resolved host.
  **CLI flag change: `-c`/`--collection` selects the
  collection; the config file is now `-C`/`--config`** (they were swapped). The
  global `-c`/`-C` are peeked from `os.Args` before cli parsing in
  `parseGlobalFlags` (the command tree is built from config first).
- **Reverse-relation config.** `ReverseRelations` sets which relations (value1)
  every collection indexes in reverse; omit it for the built-in default
  (`tag`, `path`, `parent`, `tie-type`, `version-of`). `version-of` powers
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
`mount` (FUSE), `dump`/`restore` (TSV backup). There is no `put` — use `upload`.
`tie upload <file>` prints the content hash. `mount --db <mnt>` for the live tag
tree; `mount <hash> <mnt>` for an immutable content-addressed dir.

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

## FUSE read path (perf-relevant)

`mount` → `NewTieDBFuse`/`NewTieFuse` with a `--cache` size in GB (default 1).
The `cache` is **disk-backed and single-flight**: on the first read of a hash it
`GET`s the blob once, streams it to a file under a temp dir (`io.Copy` — memory
is bounded by the copy buffer, not the file size), then serves reads via
`ReadAt` on the open fd. Concurrent reads of the same hash coalesce on a
per-entry `ready` channel (one download regardless of reader count). The
`--cache` budget is an LRU eviction target, not a hard limit — a file larger
than the budget still reads fine (blobs are unlinked after open, so an evicted
entry stays readable through fds that already resolved it). `TieFuse.Close()` /
`TieDBFuse.Close()` remove the temp dir; the `mount` command defers it, so a
normal unmount cleans up (a `kill -9` can't run the defer and leaves a
`/tmp/tie-fuse-cache-*` dir). The filehost serves blobs with `http.ServeFile`
from a sharded dir tree (`data/<xx>/<yy>/<hash>`).

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
