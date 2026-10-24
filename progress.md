# Progress: local `.tie` database support (`tie -f/--file`)

Detailed design: `docs/local-db-plan.md`.

Status: **implemented** (milestones 1–6 and the read-only, reverse-index and
tie-smb follow-ups). Full test suite and `-race` on the affected packages pass;
`go vet` and `staticcheck` are clean. The tie-gui setting remains blocked on
that repo's v0.6.0 client migration.

## Milestone 1 — transport seam (no behavior change)

- [x] `webservice.Doer` interface (`Do`, `DoStream`, `SetTimeout`)
- [x] `(*Client).Do` / `(*Client).DoStream`; `RunStreamContext` shares
      `runStreamBody`
- [x] `Call` / `CallContext` accept a `Doer`
- [x] `TieClient.client` → `TieClient.transport`
- [x] update `callCtx`, `DumpStream`, `In`, `NewTieClientFor`, `SetTimeout`
- [x] `scope_test.go` follows the field rename
- [x] `go test ./webservice ./client ./cmd/tie` green

## Milestone 2 — in-process dispatch

- [x] `WebserviceConfig.LocalDir`
- [x] local branch in `collectionKey` (collection validated, namespace ignored)
- [x] shared registry dispatch (`serve`)
- [x] `Dispatch` / `DispatchStream` + `localResponseWriter` / `trackingWriter`
- [x] `registration.stream` for stream handlers (pre-body errors stay errors)
- [x] `localTransport` (`client/local.go`): `Do`, `DoStream` via `io.Pipe`,
      status-error mapping, context checks
- [x] tests: round trip, stream, pre-body error, unknown request, cancellation

## Milestone 3 — locking and durability

- [x] `TieTree.SetLocking` (engine default off; `NewWebservice` default on)
- [x] `flock(LOCK_EX|LOCK_NB)` unix implementation + no-op fallback
- [x] acquire in `initialize`, release in `closeDB`/load failure; idempotent
- [x] `DropCollection` locks an unloaded file before removal
- [x] `DisableLocking` in `WebserviceConfig` and triplestore TOML
- [x] `internal/testenv` calls `ws.Close()` in cleanup; exposes `Stop`/`DBDir`
- [x] tests: second open refused, release on Close, locking-off default

## Milestone 4 — client local mode

- [x] `Config.TripleStoreFile`, `ResolvedCollection.TripleStoreFile`
- [x] `Config.ResolveCollection` maps collections to sibling files
- [x] `ConfigForFile` (`.tie` required, existing regular file, symlink-resolved)
- [x] `NewTieClientFor` / `In` local branches (siblings share one backend)
- [x] `Close`, `Target`, `IsLocal`, `Err`
- [x] media schema cache key includes the file path
- [x] parity tests: server-written DB vs direct open (queries, dumps, hash
      blob policy), reverse-index superset, sibling collections, lock refusal
- [x] validation tests (missing/non-`.tie`/directory)

## Milestone 5 — CLI

- [x] `parseGlobalFlags` learns `-f/--file` (and `configExplicit`)
- [x] `configureDBFile`: `-f` wins, missing default config is fine, explicit
      broken config errors, filehosts/ImportDest/PrevVersions preserved
- [x] `app` setup error path; `before` reports `TieClient.Err()`
- [x] close on exit (normal and error)
- [x] engine slog routed through the CLI sink at warning level (`-v` for detail)
- [x] `tie version` local row + skip filehost probes with no config
- [x] `tie conf show` `db-file` row / `file:` target
- [x] `dump --file` takes the lock and blob policy, closes the DB, and is
      config-exempt
- [x] end-to-end tests: add/get/dump/conf/version, offline dump, flag parsing,
      precedence, running-server lock refusal

## Milestone 6 — docs

- [x] `README.md`: flag, offline section, filehost caveat, dump change
- [x] `CLAUDE.md`: direct mode, config, locking, triplestore key
- [x] `docs/internals.md`: direct-mode design and invariants
- [x] `CHANGELOG.md`: Added/Changed
- [x] help text (root flag)
- [x] `docs/local-db-plan.md`

## Follow-ups

### Read-only / shared-lock reading

- [x] `TieTree.SetReadOnly` / `Collection.readOnly`: private read-only fd, no
      writer goroutine, no advisory lock, no truncation.
- [x] Loader stops at the last whole frame (`loadPass` limit, `initReadSize`), so
      a live writer's partial trailing record is ignored.
- [x] `ErrReadOnly` guards `Add`/`Delete`/`MkDir`/`DropCollection`; `CheckIndex`
      refuses repair on a snapshot.
- [x] `WebserviceConfig.ReadOnly`: no lock, write requests answered 403 (both
      `RequestHandler` and `Dispatch`/`DispatchStream`).
- [x] `CheckIndex` diagnostic is allowed through `SetReadOnlyGuard`; a repair is
      refused, so `tie verify --read-only` works.
- [x] `Config.ReadOnly` + CLI `--read-only` (requires `-f`).
- [x] Tests: engine read/write coexistence, missing file creates nothing,
      client read beside a writer, CLI read-only, writer still works.
- [x] Manual: `tie -f … mount --db` running, then
      `tie -f … --read-only get k1` succeeds.

### Reverse-relations knob

- [x] `Config.ReverseRelations` / `WebserviceConfig.ReverseRelations` plumbing.
- [x] CLI `--reverse-relations a,b,…` (requires `-f`), comma/whitespace parsing.
- [x] `conf show` reports `read-only` and `reverse-relations`.
- [x] Tests: a subset omits the reverse index for an excluded relation; the full
      set restores it.

### tie-smb

- [x] `TripleStoreFile` and `ReadOnly` in `SMBConfig`.
- [x] `applyDirectConfig` applies them over the loaded tie config.
- [x] `ReadOnly` downgrades every SMB user to read-only and closes the client on
      shutdown.
- [x] Startup logging uses `tie.Target()`.
- [x] Docs + example config + unit test.

### tie-gui — blocked (dependency migration, not a tie change)

The GUI repo is pinned to `github.com/uidbz/tie v0.6.0` and does **not** compile
against the current client façade (the v0.7 collection-scope/API changes:
`WriteFileWithProgress` signature, `Tie*` constants, `UploadMultipart`, …). A
`TripleStoreFile` setting there needs that migration first; the `client` API is
ready (`ConfigForFile`, `ReadOnly`, `ReverseRelations`). See the note in
`docs/local-db-plan.md`.

## Verification log

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go test ./...` | all packages pass |
| `go test -race ./tiedb ./client ./cmd/tie ./cmd/tie-smb ./webservice` | pass |
| `go vet ./...` | clean |
| `go tool staticcheck ./...` | clean |
| manual `tie -f … conf show` / `version` / `dump` / `get` | works, no network, no log noise |
| manual `tie -f Main.tie -c Main_prev conf show` | sibling file resolved |
| manual `tie -f … add` then a new process `get` | write flushed and visible after Close |
| manual `tie dump --file` after closing the client | reads the flushed write |
| manual offline (no config, `HOME` empty) | works; version does not probe a server |
| manual `tie -f … mount --db` + `tie -f … --read-only get` | reader coexists with the mount |
| manual `tie-smb` with `TripleStoreFile`/`ReadOnly` | starts read-only, all users downgraded, clean shutdown |
| `make build` | all four commands build with ldflags |

## Remaining optional follow-up

- Optional typed in-process dispatch without JSON, if profiling shows it matters.

