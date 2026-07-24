# tie

`tie` is a collection of Go programs and libraries built around a triple store,
focused on **tagging and content-addressed storage**: files live in a
content-addressed blob server, their tags and metadata live in the triple store,
and the file's content hash is the key that joins the two.

The store itself stays general — it holds arbitrary `(key, value1, value2)`
triples — but the tooling is oriented toward tagging files and browsing them as
a virtual filesystem.

For an end-to-end walkthrough of running tie as a personal media library —
importing a filesystem, tagging albums/series/galleries, querying media by tag
combinations, and linking media together — see
[docs/media-database.md](docs/media-database.md).

## Programs (`cmd/`)

| Program        | What it does |
|----------------|--------------|
| `tie-daemon`   | Web service exposing a tie triple store over HTTP. |
| `tie`          | CLI client for a `tie-daemon`: add/get/delete triples, import & tag files, upload/download, dump/restore, and mount. |
| `tie-filehost` | Content-addressed file server. Stores file bytes and immutable directory (`tiedir`) blobs, addressed by highwayhash. |

## Packages

| Package       | What it does |
|---------------|--------------|
| `tiedb`       | In-memory triple store, persisted to disk. See [docs/internals.md](docs/internals.md) for its data structures, file format, and memory model. |
| `client`      | Talks to a `tie-daemon`; also the tagging / virtual-directory layer. |
| `api`         | Request/reply types shared by client and server. |
| `webservice`  | HTTP transport and auth for the daemon. |
| `metadata`    | The `tiedir` directory-blob format (headers, entry lines, media-type detection). |
| `io/putlib`   | Upload files to a `tie-filehost`. |
| `io/getlib`   | Download files from a `tie-filehost`. |
| `io/fuselib`  | Mount tie directories as a FUSE filesystem (the live `--db` mount is read-write: rename, `mkdir`, and tag editing). See [docs/mount.md](docs/mount.md). |

## The data model

Everything in `tiedb` is an **association** — a triple of `key`, `value1`,
`value2`. The idea is that nothing stands alone; a thing only means something in
relation to another thing. `key` is what you look something up by; `value1` is
the relation; `value2` is the associated value. For example:

```
pizza  topping  cheese
pizza  topping  basil
pizza  baking-time  7 min
```

A query returns a `TripleSet` — a map of maps of maps
(`key → value1 → value2`) — with helper methods for reading results ergonomically.
Order is not retained.

### Example

```go
package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)

	add := func(key, value1, value2 string) {
		if _, err := tie.Add(key, value1, value2); err != nil {
			fmt.Println("Error adding triple:", err)
		}
	}

	add("pizza", "topping", "tomato")
	add("pizza", "topping", "cheese")
	add("pizza", "topping", "basil")
	add("pizza", "baking-time", "7 min")
	add("pizza", "baking-temperature", "250 °C")
	tie.Sync()

	reply, err := tie.Get("pizza", client.GetOptions{})
	if err != nil {
		fmt.Println("Error getting result:", err)
		return
	}

	// All value2s under a given value1 ("category").
	reply.Result["pizza"]["topping"].ForEach(func(value2 string) {
		fmt.Println(value2)
	})

	// A single expected value2.
	if value2, ok := reply.OneValue2("baking-temperature"); ok {
		fmt.Println(value2)
	}
}
```

Client methods return `(reply, error)`; `error` is non-nil for both transport
failures and API-level failures. `client.ErrNotFound` distinguishes "no such
key" from a real error — check it with `errors.Is`.

More runnable examples live in `examples/`.

## Content-addressed storage and `tiedir`

`tie-filehost` stores each file under its content hash, so identical content is
stored once. A **directory** is stored as a `tiedir` blob: an immutable listing
of `(hash, filename, size, head)` entries. Because the blob's own hash changes
whenever its contents change, directories are versioned like git trees. The
`metadata` package defines this format and can recover a file's media type from
the stored head bytes.

Directory entries store only a child's **basename**, never a path. A directory's
hash is therefore a pure function of its contents, so the same tree dedupes
regardless of where it was uploaded from, and checkout rebuilds the layout
relative to the destination the caller chooses. The tree's location lives in the
parent entry (or in the checkout `dest` for the root), the same way git trees
work.

### Transfer integrity and the untrusted filehost

The download path treats the filehost as **untrusted**: it may be compromised,
buggy, or reached over a tampered connection. `metadata` is the single source of
the highwayhash content-address algorithm (`metadata.NewHash` / `HashReader`),
so upload hashing and download verification cannot drift apart.

- **Content is verified on receipt.** `io/getlib` recomputes the hash of every
  downloaded blob and rejects it (`getlib.ErrChecksum`) if it does not match the
  address it was requested under, so a tampered or truncated `download`/`import`
  transfer never reaches disk as if it were genuine. Large files stream through
  the hasher rather than being buffered whole.
- **FUSE reads verify on request.** The `mount` cache in `io/fuselib` can hash
  every downloaded blob the same way, but does **not** by default: for a trusted
  personal filehost the per-read hash pass over multi-GB media is wasted work.
  Pass `tie mount --verify` to turn it on (the blob then streams through the
  hasher and a mismatch is rejected); use it when the filehost is untrusted.
- **Manifests cannot smuggle traversal or SSRF payloads.** `metadata.ParseDirLine`
  accepts an entry only if its filename is a single safe path component (no
  separators, not `.`/`..`) and its hash is exactly 64 lowercase hex characters.
  A crafted manifest therefore cannot escape the checkout root or redirect a
  fetch to an arbitrary endpoint.
- **Traversal is bounded.** Content addressing makes honest cycles impossible,
  and a malicious self-referential manifest fails verification; as a further
  backstop, eager directory recursion in `getlib` is capped at a fixed depth.

## The `tie` CLI

```
tie add <key> <value1> <value2>      add a triple
tie get [-r] [-f value1] <key>       query triples (reverse, filtered)
tie del <key> <value1> <value2>      delete triples
tie import ... [--collection name]   upload and tag files into a collection
tie upload <file>                    upload a file/dir to a filehost
tie download <hash> <dest>           download a file/dir from a filehost
tie dump [--file <path.tie>]         export the collection as TSV (--file reads a local .tie directly)
tie restore                          import TSV (additive)
tie mount <hash> <mountpoint>        mount an immutable content-addressed tree
tie mount --db <mountpoint>          mount the live tag-derived filesystem
tie conf create [name]               write a default config file
```

`tie -c <config>` selects a config file (searched in the working directory
first). Run `tie conf create` to generate one.

### Filehosts and config

`tie` connects to a `tie-daemon` (the `Webservice`) and one or more
`tie-filehost` servers. Filehosts are named in config:

```toml
Webservice = 'https://localhost:1161'
DefaultFileHosts = ['default']

[FileHosts.default]
URL = 'https://localhost:1162'
Insecure = false   # true skips TLS certificate verification (self-signed certs)
```

`upload`, `download`, `import`, and `mount` select a filehost with `--host
<name>` (defaulting to the first `DefaultFileHosts` entry). `upload`/`download`
also accept `--server <url>` to target a raw filehost URL without config, plus
`--insecure` to skip TLS verification for that address. The scheme lives in the
URL — `Insecure` only controls certificate checking, it does not switch
`http`/`https`.

### Mounting

`tie mount <hash> <mountpoint>` mounts a `tiedir` blob as a read-only,
content-addressed tree.

`tie mount --db <mountpoint>` mounts a live filesystem derived from the triple
store, with three top-level trees:

- **`query/`** — a directory named after a tag query lists the matching files —
  e.g. `query/jazz mellow -live` ANDs `jazz` and `mellow` and excludes `live`,
  and a `type:` token scopes to a media type; `cat query/tags` lists every known
  tag. Saved queries from the config's `[Queries]` table appear here as
  ready-made directories. Read-only.
- **`files/`** — the path-based import tree (`file:/...`), browsable directly.
  **Writable**: `mv` renames or moves a file (updating its `filename`/`parent`
  triples) or a directory (cascading the path over its descendants), and `mkdir`
  creates a new directory.
- **`tags/`** — mirrors `files/`, but each leaf is a small **writable text file**
  whose contents are that file's tags, one per line. `cat` shows the current
  tags; writing the file replaces the set (empty clears all).

It reflects the store on every directory read, so re-tagging shows up without
remounting. A tagged directory appears as a real directory and expands into its
immutable `tiedir` snapshot. Names and tags attach to the content hash, so an
edit is visible everywhere that content appears. See
[docs/mount.md](docs/mount.md) for the full mount reference and the technical
implementation of the write paths.

Both mounts fetch bytes from the filehost on demand and keep them in an
**on-disk, single-flight cache**: a blob is downloaded once (concurrent readers
of the same file coalesce onto that one download), streamed to a temp file so
memory stays bounded regardless of file size, and served from there. `--cache
<GB>` sets the eviction budget (default 1); it is an LRU target, not a hard cap,
so a file larger than the budget still reads. The cache is removed on unmount.
`--verify` turns on content-hash verification of downloaded bytes (off by
default — see *Transfer integrity* above).

### Backup / interop

```sh
tie dump    > backup.tsv     # every triple as key<TAB>value1<TAB>value2
tie restore < backup.tsv     # additive, idempotent merge
```

Restore re-adds triples via a batch; adding an existing triple is a no-op, so it
merges rather than replaces. Point `-c` at a config with a different collection
to copy data between collections.

`tie dump --file <path.tie>` exports directly from an on-disk `.tie` file
without a running daemon, for offline backup:

```sh
tie dump --file /var/lib/tie/myns/mycol.tie > backup.tsv
```

It reflects flushed on-disk state only, so do not run it against a `.tie` file
that a live `tie-daemon` currently has open — the daemon may hold unflushed
writes, and no locking coordinates the two readers.

## Building

```sh
go build ./...
```

Requires Go 1.25+. Mounting additionally requires FUSE (`/dev/fuse`,
`fusermount`).

### Makefile

Common tasks are wrapped in a `Makefile`:

| Command | What it does |
|---------|--------------|
| `make build` | Build all commands into `dist/`. |
| `make install` | Install `tie-daemon` + `tie-filehost` as system services (systemd/OpenRC). Run as `sudo make install`. See [Running as system services](#running-as-system-services). |
| `make clean` | Remove `dist/`. |
| `make tls-keys` | Generate a self-signed `localhost.crt`/`localhost.key` for `tie-daemon` / `tie-filehost`. |
| `make push MSG="message"` | `go get -u . && go mod tidy`, then commit everything and push. |
| `make release VERSION=<tag>` | Tag `<tag>` and push it (e.g. `make release VERSION=v0.4.0`). |

`push` and `release` require their argument and abort with a usage message if
it is missing. `make build` embeds the version (from `git describe`, or an
explicit `VERSION=`) into the binaries, reported by `tie --version`,
`tie-daemon -version`, and `tie-filehost -version`.

Release notes are kept in [CHANGELOG.md](CHANGELOG.md).

## Deployment

`tie-daemon` (triple store, port 1161) and `tie-filehost` (blob store, port
1162) are two separate services. TLS is normally terminated by a reverse proxy
(nginx / Caddy) in front of them: run with `Insecure = true` bound to
localhost, and let the proxy handle certificates. Alternatively set
`CertFile`/`KeyFile` in each config to serve HTTPS directly.

### Running as system services

The installer builds both binaries, creates a dedicated `tie` user, lays down
config in `/etc/tie/` and data in `/var/lib/tie/`, and installs service files
for whichever init system is detected (systemd or OpenRC):

```sh
sudo make install      # or: sudo ./contrib/install.sh
```

Then edit `/etc/tie/tie-daemon.toml` (at least the `[[Users]]` account) and
start the services (`systemctl start tie-daemon tie-filehost`, or
`rc-service tie-daemon start`). Re-running the installer to upgrade binaries is
safe — existing config is never overwritten. See
[contrib/README.md](contrib/README.md) for the full reference.

### Docker

The `Dockerfile` at the repo root builds a single image that runs **both**
services together:

```sh
docker build -t tie .
docker run -p 1161:1161 -p 1162:1162 -v tie-data:/data tie
```

On first run it generates default configs under `/etc/tie` pointing at the
`/data` volume (daemon db in `/data/db`, filehost blobs in `/data/data`). Set
`TIE_USER` / `TIE_PASSWORD` to seed the daemon's initial account, or mount your
own `/etc/tie` to override the configs entirely.
