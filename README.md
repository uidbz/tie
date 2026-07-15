# tie

`tie` is a collection of Go programs and libraries built around a triple store,
focused on **tagging and content-addressed storage**: files live in a
content-addressed blob server, their tags and metadata live in the triple store,
and the file's content hash is the key that joins the two.

The store itself stays general — it holds arbitrary `(key, value1, value2)`
triples — but the tooling is oriented toward tagging files and browsing them as
a virtual filesystem.

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
| `io/fuselib`  | Mount tie directories as a FUSE filesystem. |

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

- **Content is verified on receipt.** `io/getlib` and `io/fuselib` recompute the
  hash of every downloaded blob and reject it (`getlib.ErrChecksum`) if it does
  not match the address it was requested under. A tampered or truncated transfer
  never reaches disk, the cache, or a FUSE reader as if it were genuine. Large
  files stream through the hasher rather than being buffered whole, and cache
  writes are atomic (temp file + rename) so a failed transfer leaves no corrupt
  entry.
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

`tie mount --db <mountpoint>` mounts a live filesystem derived from tags in the
triple store, laid out as `by-tag/<tag>/<file>`. It reflects the store on every
directory read, so re-tagging shows up without remounting. A tagged directory
appears as a real directory and expands into its immutable `tiedir` snapshot.

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
