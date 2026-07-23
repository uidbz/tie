# tie

`tie` is a client for a triple-store (`tie-daemon`) plus a content-addressed
blob store (`tie-filehost`). The CLI uploads/downloads files, tags them, and
mounts collections as a FUSE filesystem.

## Layout

| Path                 | What |
|----------------------|------|
| `cmd/tie/`           | CLI (`commands.go` has every subcommand). |
| `cmd/tie-daemon/`    | Triple-store server. TOML-configured. |
| `cmd/tie-filehost/`  | Content-addressed blob store. TOML-configured. |
| `client/`            | `TieClient` — the Go API the CLI and fuselib call. |
| `io/fuselib/`        | FUSE. `fuselib.go` = content-addressed mount; `fuselib_db.go` = live tag-derived mount (`--db`). |
| `io/putlib/`, `io/getlib/` | Upload / download plumbing. |
| `tiedb/`             | The triple-store engine (association index; memory-sensitive — see auto-memory). |
| `test-env/`          | Local end-to-end sandbox. Start here to run anything. |

## Running the stack (test-env)

Everything is localhost, plaintext HTTP. Daemon `:1161`, filehost `:1162`.

```bash
cd test-env
./build.sh        # go build all three binaries into bin/
./start.sh        # start daemon + filehost, generate their TOML configs if absent
./seed.sh         # upload + tag sample files
./mount-db.sh     # mount live tag tree at mnt/ (foreground, Ctrl-C to unmount)
./stop.sh         # unmount + kill both services
```

- `env.sh` holds all shared paths/ports and the `tie_cli` helper. Source it, then
  use `tie_cli <args>` (it `cd`s into test-env and passes `-c config.toml`).
- **Both servers are TOML-configured** (`-config <file>`), not flags. `start.sh`
  generates `tie-daemon.toml` and `tie-filehost.toml` if missing. Both are
  gitignored runtime artifacts; only `config.toml` (the CLI config) is tracked.
- Filehost config keys: `ListenOn`, `Insecure`, `DbPath`, `CertFile`/`KeyFile`,
  `ReapInterval` (Go duration; `"0"` disables the expired-blob reaper).
- The daemon's DB load can be slow on a large `db/`, so `start.sh`'s readiness
  loop may run for a while before both ports answer — that's expected.

## CLI commands

`add`/`del`/`get` (triples), `upload`/`download` (files), `import` (dir trees),
`mount` (FUSE), `dump`/`restore` (TSV backup). There is no `put` — use `upload`.
`tie upload <file>` prints the content hash. `mount --db <mnt>` for the live tag
tree; `mount <hash> <mnt>` for an immutable content-addressed dir.

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

## Conventions

- tiedb diagnostic/log output goes to **stderr**, never stdout (stdout is data,
  e.g. `dump` TSV).
- For tiedb changes, memory footprint is weighted ≥ load speed; benchmark old vs
  new under the same conditions before keeping a perf change.
- filehost stays a pure content-addressed byte store — no media/format parsing.
