# tie test environment

A local sandbox for running the full `tie` stack end to end: the triple-store
daemon, the content-addressed filehost, some sample data, and FUSE mounts.
Everything runs on localhost over plain HTTP — no TLS, no external services.

## Requirements

- Go (to build the binaries)
- FUSE: `/dev/fuse` and `fusermount` (for the mount commands)
- `curl` (start.sh uses it for readiness checks)

## The 60-second tour

```bash
cd test-env     # from the repo root
./build.sh      # compile the tie binaries into ./bin
./start.sh      # launch the daemon (:1161) and filehost (:1162)
./seed.sh       # upload + tag sample files and a directory
./mount-db.sh   # mount the live tag-derived filesystem at ./mnt
```

Then, in another terminal:

```bash
ls  mnt/by-tag/                        # finance  outdoors  vacation
ls  mnt/by-tag/vacation/               # beach.txt  holiday-pics/
cat mnt/by-tag/vacation/beach.txt
ls  mnt/by-tag/vacation/holiday-pics/  # a tagged dir, expanded from its tiedir blob
```

Press Ctrl-C in the mount terminal to unmount, then `./stop.sh` to shut the
services down. Or run `./demo.sh` to do the whole thing non-interactively and
tear it all down at the end.

## Scripts

| Script          | What it does |
|-----------------|--------------|
| `build.sh`      | Build `tie`, `tie-daemon`, `tie-filehost`, `tie-upload`, `tie-download` into `bin/`. |
| `start.sh`      | Start the daemon and filehost in the background (insecure HTTP), wait until both accept connections. |
| `stop.sh`       | Unmount `mnt/` if mounted, then stop both services. |
| `seed.sh`       | Create sample files, upload their bytes to the filehost, and write the tag triples the DB mount reads. |
| `mount-db.sh`   | Mount the live, tag-derived filesystem at `mnt/` (foreground; Ctrl-C to unmount). |
| `mount-hash.sh` | Mount an immutable content-addressed directory by its tiedir hash: `./mount-hash.sh <hash>`. |
| `backup.sh`     | Dump the current collection to TSV, or restore a TSV back in. |
| `demo.sh`       | One-shot: build → start → seed → browse → unmount → stop. |
| `env.sh`        | Shared paths, ports, and the `tie_cli` helper. Sourced by every script. |

## Two ways to mount

**`mount --db` (live, mutable).** The virtual tree is derived from the triple
store on every `readdir`, so tagging changes show up without remounting:

```bash
./mount-db.sh
# in another shell — tag a file while it's mounted:
tie -c config.toml add <hash> tag newtag
tie -c config.toml add tags all newtag
ls mnt/by-tag/          # newtag/ appears on the next listing
```

Layout: `mnt/by-tag/<tag>/<file>`. A tagged **directory** appears as a real
directory and expands into its immutable tiedir snapshot from the filehost.

**`mount <hash>` (immutable).** `seed.sh` prints the tiedir hash of the sample
`holiday-pics/` directory. Mount it directly:

```bash
./mount-hash.sh <dir-hash>
ls mnt/           # pic1.txt  pic2.txt
```

## Backup and restore

Export every triple in the current collection as tab-separated
`key<TAB>value1<TAB>value2`, and restore it (additively) anywhere:

```bash
./backup.sh dump  backups/main.tsv     # or: ./backup.sh dump -   (to stdout)
./backup.sh restore backups/main.tsv   # merges into the current collection

# raw CLI:
tie -c config.toml dump    > backup.tsv
tie -c config.toml restore < backup.tsv
```

Restore is additive and idempotent — re-adding an existing triple is a no-op,
so it merges rather than replaces. To copy data between collections, point `-c`
at a config with a different `Collection` value.

## How it's wired

- **Daemon** (`tie-daemon`, `:1161`) — the triple store. Data in `db/`. It
  auto-creates the user `defaultuser` / `defaultpassword`, which is what
  `config.toml` authenticates as.
- **Filehost** (`tie-filehost`, `:1162`) — content-addressed blob store. Data
  in `data/`. Files are addressed by their highwayhash; directories are stored
  as `tiedir` blobs.
- **`config.toml`** — the `tie` CLI config, pointing both endpoints at local
  HTTP. The CLI resolves `-c` relative to the working directory, so the scripts
  `cd` here and pass the bare filename.

## Directories

| Path            | Contents |
|-----------------|----------|
| `bin/`          | Built binaries. |
| `db/`           | Daemon triple-store state. |
| `data/`         | Filehost content-addressed blobs. |
| `mnt/`          | FUSE mountpoint. |
| `sample-files/` | Generated inputs for `seed.sh`. |
| `logs/`         | Server logs and PID files. |
| `backups/`      | TSV dumps (created by `backup.sh`). |

## Resetting

Stop everything, then wipe the state directories for a clean slate:

```bash
./stop.sh
rm -rf db data mnt/* backups
```

## Caveats

- Everything is plaintext HTTP on localhost — do not expose these ports.
- Run the mount commands only while the services are up; the FUSE tree fetches
  bytes from the filehost on demand.
