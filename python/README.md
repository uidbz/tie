# tie-client

A pure-Python client and CLI for the [`tie`](../) triple-store (`tie-triplestore`)
and content-addressed blob store (`tie-filehost`). No third-party
dependencies — it uses only the standard library (`urllib`, `tomllib`, `ssl`,
`zipfile`, `struct`) and targets Python **3.11+**.

The wire format, tag vocabulary, config layout, and directory-manifest bytes
are all matched to the Go client (`../client/`), so files, tags, and directory
trees written by this client interoperate byte-for-byte with the Go `tie` CLI
and vice-versa.

## Install

```bash
cd python
pip install -e .          # installs the `tie` console script
```

This registers a `tie` entry point (`tie_client.cli:main`). If you already
have the Go `tie` on your `PATH`, install into a venv or invoke the module
directly: `python -m tie_client.cli ...`.

## Configuration

Config is TOML, matching `client.Config` in the Go client. It is searched for
by name (default `config`, `.toml` appended if missing) in this order:

1. the name as a literal path,
2. `./<name>` (current directory),
3. `$XDG_CONFIG_HOME/tie/<name>` (or `~/.config/tie/<name>`).

Create a localhost default and edit it:

```bash
tie conf create           # writes ~/.config/tie/config.toml
```

```toml
Username   = 'defaultuser'
Password   = 'defaultpassword'
Namespace  = 'Collections'
Collection = 'Main'
Webservice = 'http://localhost:1161'
DefaultFileHosts = ['default']
PrevVersions = 3

[FileHosts.default]
URL = 'http://localhost:1162'
Insecure = false
# Username / Password   # optional filehost Basic-Auth creds

[ImportDest]              # optional: import destination templates per dir-type
audio-dir = '{artist}/{album}'

[Queries]                 # optional: saved named queries
```

Key fields:

- **`Webservice`** / **`WebserviceInsecure`** — triplestore URL; `Insecure` is
  `InsecureSkipVerify` for HTTPS (does not swap the scheme).
- **`FileHosts.<name>`** — blob stores, keyed by name. `DefaultFileHosts[0]`
  is used when no `--host` is given. `Username`/`Password` ride every filehost
  request as Basic Auth.
- **`PrevVersions`** — version-history retention count. Note: a TOML file that
  *omits* `PrevVersions` loads `0` (no history), matching the Go client —
  only `conf create` seeds the default of `3`.

> **Gotcha:** `default_config()` points at `localhost:1161/1162`. For any real
> deployment, load an explicit config; do not rely on the built-in default.

## CLI

Mirrors the non-FUSE subcommands of the Go `cmd/tie` (there is no `mount` — use
the Go binary for FUSE). Pass `-c <name>` to select a config.

### Triples

```bash
tie add <key> <value1> <value2>          # add a triple  (alias: a)
tie get <key> [more...] [-r] [-f REL]    # query triples (alias: g)
tie del <key> <value1> <value2>          # delete a triple (alias: d)
```

`get` treats trailing args as extra include-terms (a leading `-` makes a term
an *exclude*); any extra term implies `--reverse`. Flags: `-r/--reverse`,
`-f/--filter`, `-l/--limit` (default 1000), `-o/--offset`, `-s/--sortby`.

`del` supports wildcards: `del <key> * *` deletes every triple on the key;
`del <key> <rel> *` deletes every value under one relation. (`del <key> * <v2>`
is rejected as a likely typo.)

### Files

```bash
tie upload <path> [--host NAME | --server URL] [--insecure] [--json]
tie download <source-hash> <dest> [--host NAME | --server URL] [--insecure]
```

`upload` prints `<hash>\t<filename>` per item (directories upload bottom-up,
one line per child plus the manifest). `--json` emits a structured result.
`download` expands directory manifests recursively into `dest`.

### Import

```bash
tie import <paths...> [-t TAG]... [--host NAME]... [--dest PATH] [--collection C]
tie import audio-dir <paths...>          # label directory roots as audio-dir
tie import image-archive <paths...>      # force archive tie-type on roots
```

`import` uploads and tags in one step: sniffs each file's tie-type (refining
zip archives to media-specific types by their modal content), extracts audio
metadata, builds the `tie:`-scheme path tree, and reconciles/versions
superseded files. Built-in dir-type subcommands are `audio-dir`, `image-dir`,
`video-dir`, `document-dir` and the four `*-archive` types; any `[ImportDest]`
key in your config adds another subcommand. `--dest` overrides placement;
otherwise an `[ImportDest]` template (e.g. `{artist}/{album}`) or the source
path is used.

### Tags

```bash
tie tag list [-o OFF] [-l LIM]     # registered tag names
tie tag add <tag>                  # register a name only
tie tag del <tag>                  # remove the tag everywhere + registry
tie tag rename <old> <new>         # rewrite the tag everywhere
tie tag files <tag>                # items carrying a tag
tie tag show <hash>                # tags on one item
tie tag set <hash> [tags...]       # replace an item's tags
tie tag untagged [-t TYPE]         # items with a tie-type but no tag
```

Tags attach to a content hash, so `del`/`rename`/`set` change the tag wherever
that content appears. `untagged` uses the server-side `MissingRelation`
predicate, so only untagged rows cross the wire.

### Versions

```bash
tie versions list <path>              # history in <Collection>_prev
tie versions restore <path> [hash]    # restore latest (or a specific) version
```

### Backup

```bash
tie dump                    # every triple as TSV on stdout
tie restore [file] [--drop] # load TSV (stdin if no file); --drop clears first
```

## Library

Everything hangs off one `TieClient` facade (mirroring the Go client — the
high-level tag/dir/import/version/favorite/cotag methods are bound onto the
class at import time). Import from the top-level package:

```python
from tie_client import TieClient, Config, FileHost, QuerySpec, Update

# From a named config file (falls back to the localhost default if absent):
tie = TieClient.from_config_name("config")

# Or build a config explicitly (recommended for non-localhost):
tie = TieClient(Config(
    username="u", password="p",
    namespace="Collections", collection="Main",
    webservice="http://host:1161",
    default_file_hosts=["main"],
    file_hosts={"main": FileHost(url="http://host:1162")},
    prev_versions=3,
))
```

### Triples & queries

```python
tie.add("key", "tag", "holiday")
row = tie.get("key")                       # -> Row; raises NotFound if absent
row.values("tag")                          # ["holiday", ...]
row.first("filename")                      # first value or ""

rows, total = tie.query(QuerySpec(
    terms=["holiday"], filter="tag", reverse=True,
    expand=True, limit=50,
))

for k, v1, v2 in tie.dump():               # streamed NDJSON of forward triples
    ...
```

`QuerySpec` fields: `terms`, `exclude`, `scope`, `missing_relation`, `filter`,
`reverse`, `expand`, `offset`, `limit`, `sort_by`. A query matching nothing
raises `NotFound` (a normal empty-result signal, not a transport error).

### Batched writes

```python
b = tie.new_batch()                        # ordered ops against one collection
b.add(hash, "tag", "beach")
b.delete(hash, "tag", "old")
b.set(hash, "tag-date", ["2026-08-20 ..."])
b.update(Update(hash, "filename", "old.jpg", "new.jpg", add_on_failure=True))
tie.run_batch(b)
```

### Files

```python
result = tie.upload("main", "/path/to/file_or_dir")   # -> UploadResult
result.items[-1].hash                                  # top-level content hash
tie.download("main", content_hash, "/tmp/out")

# Lower-level, host-object variants: upload_to / download_from / download_size.
fh = tie.filehost(tie.resolve_host("main"))            # FilehostClient
fh.upload_bytes(b"...")                                # raw blob -> hash
fh.download_bytes(hash)                                # blob bytes (errors on dir)
fh.get_retention(hash) / fh.set_retention(hash, "24h") # expiry control
```

### Tables

Store a spreadsheet/CSV as a table entity (ordered headers + rows) and read it
back as a grid, without hand-rolling a triple encoding:

```python
uid = tie.insert_table("", ["Name", "Age", "City"],
                       [["Alice", "30", "NYC"], ["Bob", "25", "LA"]])
headers, rows = tie.read_table(uid)   # (["Name","Age","City"], [["Alice",...], ...])

# Pass a stable uid to replace a table in place (idempotent re-import):
tie.insert_table(uid, headers, new_rows)
```

Empty cells are not stored and read back as `""`. Headers must be unique and
none may be named `tie-type` (it would collide with the row type marker); either
case raises an error. This targets sheet-sized
tables (hundreds–low thousands of rows). The wire model is documented in
`client/table.go`; the same encoding is used by the Go and Risor clients.

### Tags, dirs, imports, versions, favorites, relations

High-level helpers on `TieClient` include: `list_tags`, `register_tag`,
`delete_tag`, `rename_tag`, `get_tags`, `set_tags`, `files_with_tags`,
`untagged_files`; `read_tie_dir`, `mk_tie_dir_all`, `set_dir_types`,
`list_dir_types`, `rename_file`, `rename_dir`; `import_file`, `import_dir`;
`list_versions`, `restore_version`; `list_favorites`, `register_favorite`,
`unregister_favorite`; `cotags_for_query`, `relate_files`, `relations_from`.

### Errors

All raise subclasses of `TieError`: `Unauthorized` (HTTP 401),
`NotFound` (key has no associated values), `ServerError` (`Success=false`
with a real message).

## Module map

| Module | Responsibility |
|--------|----------------|
| `client.py` | `TieClient` core: triples, queries, batches, file up/download; value types (`Row`, `QuerySpec`, `Update`, `Batch`, `TaggedFile`, `VersionInfo`, `MediaRelation`). |
| `highlevel.py` | Tag/dir/import/version/favorite/cotag/relation methods bound onto `TieClient` at import. |
| `transport.py` | `TripleStoreClient` — `POST {webservice}/{Id}` with Basic Auth, retry+backoff, NDJSON streaming for `Dump`. |
| `filehost.py` | `FilehostClient` — `PUT /upload`, blob GET, recursive dir up/download, retention. |
| `config.py` | `Config`/`FileHost` model, TOML load (stdlib `tomllib`) + hand-rolled writer, search paths. |
| `vocab.py` | Wire vocabulary: `Relation`, `TieType`, archive/dir type sets, `tie:` scheme, tag-date format. |
| `filetype.py` | Magic-byte media-type / tie-type detection (stdlib subset of h2non/filetype). |
| `metadata_extract.py` | Best-effort ID3v2 (MP3) and Vorbis-comment (FLAC) tag reader. |
| `tiedir.py` | The `tiedir-v2` directory-manifest format — bytes match the Go implementation exactly so identical trees dedupe. |
| `errors.py` | `TieError` and subclasses. |
| `cli.py` | argparse-based `tie` CLI. |

## Interop invariants

These are load-bearing for cross-client compatibility — change them only in
lockstep with the Go side:

- **Triplestore request envelopes** match the JSON field casing of each `api/*.go`
  request type exactly (some fields PascalCase, some lowercase-tagged).
- **`tiedir-v2` manifest bytes** (`tiedir.py`) are byte-identical to
  `metadata/tiedir.go` — that identity is what makes identical directory trees
  content-address to the same hash.
- **The tag/type vocabulary** (`vocab.py`) mirrors the Go stringers.
- **Byte verification on download is intentionally omitted**, matching the Go
  mount's default-off behavior.

## Tests

```bash
cd python
python -m pytest                       # unit tests always run
```

`tests/test_e2e.py` runs against a live test-env (triplestore `:2161`,
filehost `:2162`) and **skips automatically** when the servers are unreachable.
To exercise it, start the sandbox first:

```bash
cd ../test-env && ./build.sh && ./start.sh
```

The e2e suite also cross-checks against the Go `tie` binary built into
`test-env/bin/` to confirm both clients agree on the wire.
