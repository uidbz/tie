# Plan: `tie -f/--file` — open a `.tie` database directly

Status: implemented. See `progress.md` for the verification log.

## Goal

Let the `tie` CLI open one local `*.tie` triplestore file in-process, with no
`tie-triplestore` process and no network, via a global `-f/--file` flag.

Every triple operation must behave exactly as it does against a server. The
content-addressed filehost is **not** opened locally: commands that need bytes
(upload, download, import, blob checks, `mount <dir-hash>`, file reads through
`mount --db`) still use `[FileHosts.*]` over HTTP.

Non-goals for this change:

- opening the filehost blob store directly;
- changing the `.tie` on-disk format;
- reimplementing query/write semantics outside `api`.

Follow-ups implemented after the first release of this plan:

- `--read-only` (no writer, no lock, snapshot reads) so a query can run beside
  a live mount/triplestore;
- `--reverse-relations` to narrow the reverse index for a very large file;
- `TripleStoreFile` / `ReadOnly` on the `tie-smb` config surface.

tie-gui's setting is blocked on that repo's v0.6.0 → v0.7+ client migration; the
`client` API is ready.

## Why the chosen design

The server is already a thin registry (`webservice` + `api.RegisterAll`) over
`tiedb`. Every `TieClient` triplestore operation funnels through
`callCtx`/`DumpStream`, which use one `*ws.Client`. Adding a transport seam and
an in-process dispatcher therefore reuses **the same handlers** and cannot drift
from server semantics.

Rejected alternatives:

- **Ephemeral loopback HTTP server** — hidden server, ports, auth store and
  goroutine lifecycle; not "no server".
- **Interface over every storage operation** — duplicates handler semantics and
  is the largest possible refactor.
- **Fake `http.RoundTripper`** — smaller diff but dishonest layering; kept as a
  fallback only.

## Architecture

### 1. Transport seam (`webservice`)

```go
// Doer is the transport one request travels over.
type Doer interface {
    Do(ctx context.Context, id string, body []byte) ([]byte, error)
    DoStream(ctx context.Context, id string, body []byte, consume func(io.Reader) error) error
    SetTimeout(time.Duration)
}
```

- `(*Client).Do` wraps `send` (retry, timeout, `StatusError` preserved).
- `(*Client).DoStream` wraps `RunStreamContext`.
- `Call`/`CallContext` accept a `Doer`; existing `*Client` callers still compile.
- `TieClient.client *ws.Client` becomes `transport ws.Doer`.

This milestone is behavior-neutral.

### 2. In-process dispatch (`webservice`)

`WebserviceConfig.LocalDir string`, when non-empty, makes
`collectionKey(namespace, collection)` return
`tiedb.CollectionKey{Database: LocalDir, Collection: collection}` and ignores
the request namespace. The collection name keeps the existing validation
pattern, so no path traversal.

```go
// Dispatch serves one registered request without HTTP. Auth is intentionally
// bypassed: filesystem permissions on the .tie file are the boundary.
func (ws *Webservice) Dispatch(reqID string, raw []byte, w io.Writer) int

// DispatchStream serves a streaming request. started=false means the handler
// failed before writing, so the transport can return a *StatusError instead of
// handing JSON to a consumer expecting triples.
func (ws *Webservice) DispatchStream(reqID string, raw []byte, w io.Writer) (started bool, err error)
```

Implementation:

- `localResponseWriter` implements `http.ResponseWriter` over an `io.Writer`
  (default 200, honors `WriteHeader`, real `Header()`).
- `Environment` is built exactly as in `RequestHandler`, with
  `writable := reg.access == auth.AccessWrite`, so reads never create
  collections.
- `registration` gains `stream func(env, raw, io.Writer) error`, populated by
  `RegisterStream`, so pre-body errors can be surfaced as errors.
- Non-2xx replies are the same `ReplyStatus` JSON; the local transport converts
  them to `*ws.StatusError`, preserving `ErrNotFound` mapping.

### 3. Local backend (`client/local.go`)

`localTransport` implements `Doer` over a `*webservice.Webservice`:

- `Do` dispatches into a buffer.
- `DoStream` uses `io.Pipe` and tracks `started`; it closes the pipe with the
  producer error so `consume` cannot hang, and returns the producer error when
  it failed before any body byte.

Backend construction:

```go
ws := webservice.NewWebservice(webservice.WebserviceConfig{
    LocalDir: dir, // ReverseRelations left nil: index every relation
})
api.RegisterAll(ws)
```

`webservice.NewWebservice` installs the hash blob policy required to load
server-written files. That is mandatory; opening with a bare `tiedb.NewDB`
panics on `loadHash`.

### 4. Collection mapping and config

`Config.TripleStoreFile string`:

- Non-empty selects direct mode; `TripleStoreURL` and credentials are ignored.
- Collection `X` resolves to `<dir>/X.tie`, the pinned collection to the exact
  file. `_prev` and sibling collections therefore work.
- One direct-mode invocation covers one directory. `In("X")` shares the backend
  when the directory matches.

```go
// ConfigForFile returns a config that opens path directly, without requiring a
// config file on disk.
func ConfigForFile(path string) (Config, error)
```

- Requires `.tie`;
- requires an existing regular file (a typo must not create an empty DB);
- canonicalizes with `Abs`/`EvalSymlinks`;
- sets `Collection`/`DefaultCollection` from the basename.

`TieClient` additions:

- `local *localBackend`;
- `Close() error` (idempotent, no-op over HTTP);
- `Target() string` and `IsLocal() bool` for reporting.

### 5. Correctness invariants

1. Always construct local mode through `webservice.NewWebservice` (blob policy).
2. Index **all** reverse relations locally (`ReverseRelations` nil). The
   reverse index is derived and not stored; the server's config is unknown to a
   file, so a subset could silently drop results.
3. Ignore the request namespace in local mode.
4. Never add direct `tiedb` calls to the client; new operations go into `api`
   so both transports serve them.
5. No on-disk format changes.

### 6. Locking and durability

- `tiedb.TieTree.SetLocking(bool)`; when enabled, `initialize` takes
  `flock(LOCK_EX|LOCK_NB)` on the `.tie` file (dedicated fd held by the
  `Collection`), released in `closeDB`/load failure. `DropCollection` locks a
  file it is about to remove.
- `EWOULDBLOCK` → clear "locked by another process (is a triplestore running?)"
  error. `ENOTSUP`/`EOPNOTSUPP` → warn once and continue (network filesystems).
- `webservice.NewWebservice` enables locking by default
  (`WebserviceConfig.DisableLocking`), so the server and a direct CLI exclude
  each other. `TieTree` unit tests build the tree directly and stay unaffected.
- `internal/testenv` cleanup must call `ws.Close()` before removing the dir.
- `cmd/tie/main.go` closes the client after `root.Run`; the writer already syncs
  every ~10s. `kill -9` can still lose the last tick, as with the server.

### 7. CLI

Global flag, before the command:

```
tie -f/--file <path.tie> get ...
tie -f/--file <path.tie> mount --db /mnt
```

`parseGlobalFlags` learns `-f`, `--file`, `-f=`, `--file=`; they join
`globalFlagWithValues` so error prefixes skip the value.

Config precedence:

1. `-f` wins.
2. An explicit `-C`/`$TIE_CONFIG` that fails to load is still an error.
3. No config found + `-f` → `ConfigForFile` defaults; nothing is written.
4. Config found + `-f` → keep `FileHosts`, `ImportDest`, `PrevVersions`; override
   the triplestore.
5. `-c` in file mode selects a sibling collection; omitted → file basename.

CLI plumbing:

- `app.dbFile`, `app.configLoaded`; distinguish config-missing from
  database-file/lock errors.
- `before`: bad `-f` path is usage (2); unreadable/locked file is operational
  (4).
- File mode points the global `slog` default at the same stderr sink as
  `cliLogger`, so `tiedb` load logs obey `-q`/`-v`.
- `tie version`: triplestore row shows `local file <path>` (`Target()`), skip
  filehost probes when no config was loaded.
- `tie conf show`: add `db-file`; show `triplestore = file:<path>`.
- `tie dump --file` stays; make it config-exempt and route it through one shared
  local opener with locking.
- `client/media.go` schema cache key includes the file path.

### Command behavior with `-f`

| Command | Behavior |
|---|---|
| `add`, `get`, `del`, `tag`, `stat`, `versions`, `media`, `history`, `playlist`, `dump`, `restore`, `verify`, non-blob `repair` | fully offline |
| `conf show`, `version` | work; report the local file |
| `upload`, `download`, `import` | triples local; bytes still need a filehost |
| `stat --check`, `verify --check-blobs`, blob repairs | triples local; HEADs go to the filehost |
| `mount --db` | local metadata; file contents stream from the filehost |
| `mount <dir-hash>` | still needs the filehost |

## Milestones

1. **Seam only** (`Doer`, `CallContext`, `TieClient.transport`): no behavior
   change, full suite green.
2. **Dispatch + local transport** (unused by CLI): `LocalDir`, `Dispatch`,
   `DispatchStream`, `registration.stream`, tests.
3. **Engine locking** (opt-in), local backend uses it, server default-on,
   testenv close, lock tests.
4. **Client local mode**: config fields, `ConfigForFile`, `In`/`Close`/`Target`,
   parity tests.
5. **CLI `-f`**: parsing, config merge, exit codes, `version`/`conf show`,
   `dump` unification, end-to-end tests.
6. **Docs + CHANGELOG**.

Every milestone is independently reviewable and must pass
`go test ./...` (and `-race` where relevant) before moving on.

## Test plan

- Seam: existing suite unchanged; a fake `Doer` proves `CallContext`.
- Handler parity: scripted request sequence against a real testenv server, then
  the same file opened locally; compare rows, totals and errors. Include every
  `api` request and the query permutations.
- Reverse-index superset: DB written by a server configured with only `tag`
  opens locally and still answers `parent` reverse queries.
- Blob policy: server-written file loads without panic.
- No-network: unroutable `TripleStoreURL`, local operations still succeed.
- Lifecycle: missing path/dir/non-`.tie` rejected; second open refused; `Close`
  flushes and is idempotent; `_prev` versioning works; `dump --file` against a
  held file fails with the lock error.
- CLI end-to-end: each command class with and without a config; `-f` + `-c`
  sibling; `tie version`; `tie import` to the testenv filehost; manual
  `mount --db`.
- `go test -race ./...`.

## Documentation

- `README.md`: flag, offline section, filehost caveat, `dump --file` change.
- `CLAUDE.md`: transport seam, local mode, CLI/config, locking, reverse-index
  rule.
- `docs/internals.md`: design and invariants.
- `CHANGELOG.md`: feature + behavioral change (a second opener is refused).
- `tie --help`, `tie conf show`.

## Risks

| Risk | Mitigation |
|---|---|
| Semantic drift | reuse `api` handlers; parity tests |
| Blob-policy panic | construct through `NewWebservice`; regression test |
| Wrong reverse results | index all relations locally |
| Concurrent writers corrupt a file | `flock`, honored by server + local |
| Lost writes on exit | `Close` in main, periodic sync |
| Flag/namespace confusion | global flag only, `.tie` required, namespace ignored, sibling `-c` documented |
| Large DB memory with full reverse index | `--reverse-relations` knob; default all for correctness |
| Reader sees a writer's in-flight state | `--read-only` stops at the last whole frame and is explicitly a load-time snapshot |
| NFS/Windows flock | `unix` build tag + no-op fallback; `DisableLocking` |
| `dump --file` behavior change | document; keep the flag |
