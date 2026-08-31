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

## What's unique about tie

Triple stores are not new (RDF, SPARQL, Datomic have used the
`(subject, predicate, object)` shape for decades), and content-addressed storage
is not new either. What tie combines is the interesting part: **a
content-addressed blob store and a live, tag-derived FUSE filesystem, joined by
the content hash.**

- **Content is the key.** A file's highwayhash is its address in the blob store
  *and* the `key` of every triple that describes it. Metadata and bytes are
  joined by identity, so the same content tagged from two places is never
  duplicated — deduplication is a property of the model, not a cleanup job.
- **Tags are a filesystem.** The `--db` mount turns tag/association queries into
  a browsable, writable directory tree: a path *is* a query, `mkdir`/rename edits
  tags, and the tree updates as the store does. You navigate your data by
  combining tags instead of by remembering where you filed something.
- **Open, multi-valued schema.** Because everything is a triple, a new relation
  (a tag you invent today, a new media attribute) needs no migration, and a key
  naturally holds many values under one relation — the common case for tagging.

The triple store underneath (`tiedb`) is a deliberately hand-built,
memory-tuned engine rather than a wrapper over SQL; the trade-offs of that
choice — and where a `triples`-over-SQLite design would have been simpler — are
discussed honestly in [docs/internals.md](docs/internals.md#why-triples-and-why-not-just-sql).

## Programs (`cmd/`)

| Program        | What it does |
|----------------|--------------|
| `tie-triplestore` | Web service exposing a tie triple store over HTTP. |
| `tie`          | CLI client for a `tie-triplestore`: add/get/delete triples, import & tag files, upload/download, dump/restore, and mount. |
| `tie-filehost` | Content-addressed file server. Stores file bytes and immutable directory (`tiedir`) blobs, addressed by highwayhash. |

## Packages

| Package       | What it does |
|---------------|--------------|
| `tiedb`       | In-memory triple store, persisted to disk. See [docs/internals.md](docs/internals.md) for its data structures, file format, and memory model. |
| `client`      | Talks to a `tie-triplestore`; also the tagging / virtual-directory layer. |
| `api`         | Request/reply types shared by client and server. |
| `webservice`  | HTTP transport and auth for the triplestore. |
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

A query returns a flat, ordered list of `client.Row`s. Each `Row` has a `Key`
and an `Attributes` map (`relation → []values`), so a result reads as
`row.Attributes["topping"]` with no nested-map navigation — the same
language-neutral shape a non-Go client parses straight from JSON.

### Example

```go
package main

import (
	"fmt"

	"github.com/uidbz/tie/client"
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

	// Get fetches one key's attributes as a flat Row (relation -> values).
	row, err := tie.Get("pizza")
	if err != nil {
		fmt.Println("Error getting result:", err)
		return
	}

	// All values under a given relation.
	for _, value2 := range client.RowValues(row, "topping") {
		fmt.Println(value2)
	}

	// The first value under a relation ("" if absent).
	fmt.Println(client.RowFirst(row, "baking-temperature"))
}
```

Queries return flat, ordered `client.Row`s — `row.Attributes["topping"]` is a
`[]string`, the same JSON shape (`{"key":...,"attributes":{...}}`) a non-Go
client parses. Use `tie.Get` to fetch one key's attributes, `tie.Query` for
tag/association searches, `tie.Expand` to fetch many keys at once, and
`tie.Set` to replace a relation's values in one op.

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

A single `tie-filehost` process can serve several **physical stores** (e.g. a
fast SSD and a bulk HDD) via `[[BlobPaths]]` in its config, so separate media
types can live on separate disks without running multiple processes or ports.
Uploads pick a store with the `Tie-Store` header (client side: a filehost's
`Store` field); downloads stay hash-only and search every store. Dedup is
per-store, and each store has its own `DefaultRetention` for uploads that don't
set one. Omit `BlobPaths` for the classic single-store `BlobPath`.

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

`tie -C <config>` selects a config file (searched in the working directory
first). Run `tie conf create` to generate one. (`-c`/`--collection` selects a
collection — see [Multiple collections](#multiple-collections) below.)

### Filehosts and config

`tie` connects to a `tie-triplestore` (the `TripleStoreURL`) and one or more
`tie-filehost` servers. Filehosts are named in config:

```toml
TripleStoreURL = 'http://localhost:1161'   # legacy 'Webservice' key still works
DefaultFileHosts = ['default']

[FileHosts.default]
URL = 'http://localhost:1162'
Insecure = false   # true skips TLS certificate verification (self-signed certs)
# Store = 'ssd'     # target a named store on a multi-store filehost (Tie-Store header)
# Username = 'alice'  # optional Basic Auth for a filehost that requires it
# Password = 'secret'
```

#### Multiple collections

One config can address several collections. Select one with `tie -c <name>`;
`DefaultCollection` is used when `-c` is omitted. Each entry may override the
top-level triplestore, namespace, credentials, and filehosts:

```toml
TripleStoreURL = 'http://localhost:1161'
DefaultCollection = 'images'

[Collections.images]
Namespace = 'Collections'
Collection = 'images'
FileHosts = ['media-ssd']

[Collections.archive]
TripleStoreURL = 'http://archive-box:1161'   # a collection on another triplestore
Namespace = 'Cold'
Collection = 'archive'
FileHosts = ['media-hdd']
```

A config with no `[Collections]` behaves exactly as before: the flat
`Namespace`/`Collection`/`DefaultFileHosts` fields define a single default
collection.

The default config uses plain `http://localhost`, matching the servers' default
of `Insecure = true`. This suits a personal library on a single PC or a small
trusted LAN. To use TLS, switch the URLs to `https://` and configure the servers
with `CertFile`/`KeyFile` (or a reverse proxy) — see the deployment notes below.
For an authenticated filehost, add `Username`/`Password` to its `[FileHosts.*]`
block; they are sent as HTTP Basic Auth with every upload and download.

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
without a running triplestore, for offline backup:

```sh
tie dump --file /var/lib/tie/myns/mycol.tie > backup.tsv
```

It reflects flushed on-disk state only, so do not run it against a `.tie` file
that a live `tie-triplestore` currently has open — the triplestore may hold unflushed
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
| `make install` | Install just the `tie` client into `GOBIN` (rootless). |
| `make install-server` | Install the client, `tie-triplestore`, and `tie-filehost` as system services (systemd/OpenRC). Run as `sudo make install-server`. See [Running as system services](#running-as-system-services). |
| `make clean` | Remove `dist/`. |
| `make tls-keys` | Generate a self-signed `localhost.crt`/`localhost.key` for `tie-triplestore` / `tie-filehost`. |
| `make push MSG="message"` | `go get -u . && go mod tidy`, then commit everything and push. |
| `make release VERSION=<tag>` | Tag `<tag>` and push it (e.g. `make release VERSION=v0.4.0`). |

`push` and `release` require their argument and abort with a usage message if
it is missing. `make build` embeds the version (from `git describe`, or an
explicit `VERSION=`) into the binaries, reported by `tie --version`,
`tie-triplestore -version`, and `tie-filehost -version`.

Release notes are kept in [CHANGELOG.md](CHANGELOG.md).

## Deployment

`tie-triplestore` (triple store, port 1161) and `tie-filehost` (blob store, port
1162) are two separate services. They default to plain HTTP (`Insecure = true`)
with `ListenOn = ":116x"`, which binds **all interfaces** (`0.0.0.0`) — every
host on your LAN can reach them, unencrypted, over HTTP basic auth. This is
intended for a personal library on a trusted home network. Bind `ListenOn` to
`127.0.0.1` for local-only access, and do not expose these ports to an
untrusted network as-is.

For TLS, either set `CertFile`/`KeyFile` in each config to serve HTTPS directly,
or put a reverse proxy (nginx / Caddy) in front, keep `Insecure = true`, and
bind `ListenOn` to localhost so only the proxy reaches the service.

### Access control

Both servers authenticate with HTTP Basic Auth and share a two-tier role model:
a user is either **read** (queries / downloads only) or **write** (full access,
which includes read). Add accounts as `[[Users]]` blocks with a `Role`; an
omitted `Role` defaults to `write`, so existing configs keep working.

```toml
AnonymousAccess = "read"   # role granted when no valid credentials are sent

[[Users]]
Username = "alice"
Password = "secret"
Role = "write"             # "write" (default) or "read"
```

`AnonymousAccess` sets what an *unauthenticated* request may do — `"write"`
(fully open), `"read"` (anonymous reads, writes need a user), or `"none"` (every
request needs a valid user):

- **`tie-triplestore` defaults to `"none"`** — it has always required a login, and
  that is unchanged. Read requests are `Query`/`Expand`/`Associated`/`CoTags`/
  `Dump`; everything else (`Add`/`Delete`/`Set`/`Update`/`Batch`/`Sync`/`Drop`)
  is a write.
- **`tie-filehost` defaults to `"write"`** — it is fully open out of the box, so
  existing tools that upload anonymously keep working. To lock it down, set
  `AnonymousAccess = "read"` (downloads stay open, uploads require a write user)
  or `"none"` (everything requires a user), and add `[[Users]]`. Uploads and
  retention changes are writes; downloads and retention reads are reads.

A rejected request returns **401** when credentials are missing or wrong, and
**403** when a valid user's role is too low. Passwords are stored in plaintext,
so keep each config file readable only by the service user (e.g. `chmod 600`).
On the client side, give each authenticated filehost a `Username`/`Password` in
its `[FileHosts.*]` block.

### Running as system services

The installer builds all three binaries (client, triplestore, filehost), creates a
dedicated `tie` user, lays down config in `/etc/tie/` and data in
`/var/lib/tie/`, and installs service files for whichever init system is
detected (systemd or OpenRC):

```sh
sudo make install-server   # or: sudo ./contrib/install.sh
```

Then edit `/etc/tie/tie-triplestore.toml` (at least the `[[Users]]` account) and
start the services (`systemctl start tie-triplestore tie-filehost`, or
`rc-service tie-triplestore start`). Re-running the installer to upgrade binaries is
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
`/data` volume (triplestore db in `/data/db`, filehost blobs in `/data/data`). Set
`TIE_USER` / `TIE_PASSWORD` to seed the triplestore's initial account, or mount your
own `/etc/tie` to override the configs entirely.
