# tie-smb — the tie filesystem over SMB2/3

`tie-smb` serves the same live, tag-derived filesystem as `tie mount --db`
(`/query`, `/files`, `/tags`) over SMB2/3, so Windows Explorer, macOS Finder
and Linux CIFS clients can browse, read and edit the library without FUSE.
It is pure Go — no Samba, no cifs-utils on the server — built on
[`github.com/go-filesystems/smb`](https://github.com/go-filesystems/smb)
(BSD-3, dialects 2.1/3.0/3.0.2, NTLMv2 + SPNEGO, per-session signing).

The tree semantics are shared with the FUSE mount: `io/tiefs` holds them,
`io/smbfs` is the SMB adapter (the twin of `io/fuselib`), and `cmd/tie-smb`
is the server binary. A `tie mount --db` mount and a `tie-smb` server can run
side by side against the same triplestore: edits made through one are visible
through the other immediately, because both re-read the store on every
operation.

## Contents

- [Running](#running) — config reference, users, ports, systemd
- [Mounting from clients](#mounting-from-clients) — Linux, Windows, macOS
- [The share](#the-share) — files, tags, query: what works where
- [Name escaping](#name-escaping-read-this-first) — how odd tie names appear
- [Case, timestamps](#case) — what SMB does and does not carry
- [Security and roles](#security-and-roles)
- [Limits and known behaviors](#limits-v1)
- [Troubleshooting](#troubleshooting)
- [Testing](#testing)

## Running

```sh
cp cmd/tie-smb/tie-smb.toml.example tie-smb.toml   # edit: ListenOn, TieConfig, [[Users]]
./tie-smb -config tie-smb.toml
./tie-smb -version                                  # build info, then exit
```

Build it with the rest of the suite (`make build` puts it in `dist/`), or
`go build ./cmd/tie-smb`. `contrib/install.sh` (run via `sudo make
install-server`) installs the binary, the config and the systemd unit.

### Configuration reference

All settings live in one TOML file. **TOML note:** every key after a
`[[Users]]` header belongs to that table, so keep the plain `key = value`
settings above the `[[Users]]` section.

| Key | Default | Meaning |
|---|---|---|
| `ListenOn` | `":445"` | SMB transport address. 445 is the standard port; any high port works too (see ports below). |
| `ServerName` | `"tie"` | Name reported in NTLM challenges (cosmetic; Explorer/Finder show it). |
| `ShareName` | `"tie"` | The single exported share name. |
| `TieConfig` | `"config.toml"` | The tie client config — the same file `tie -C` takes (triplestore URL, credentials, namespace, collections, filehosts). A bare name searches the usual config dirs; a path loads that file directly. |
| `Collection` | `""` | Entry in the tie config's `[Collections]`; empty uses the config's default collection. |
| `Host` | `""` | Filehost name (from the tie config's `FileHosts`) that serves blob reads and receives uploads. Empty selects the collection's first filehost. |
| `CacheSizeGB` | `1` | On-disk blob cache budget (gibibytes) for reads served out of the filehost. LRU-evicted; lives under `$TMPDIR`. |
| `Verify` | `false` | Hash-check downloaded bytes against their content address (turn on when the filehost is untrusted). |
| `LogFile` | `""` | Also append structured JSON logs here (stderr always gets pretty output). |
| `LogLevel` | `"info"` | `debug`, `info`, `warn`, or `error`. |
| `[[Users]]` | — | One table per account: `Username`, `Password`, `Role`. |

### Users and roles

At least one `[[Users]]` entry is required — there is no anonymous mode.
Roles follow the same model as tie-filehost (`Role = "read"` or `"write"`;
empty means write):

```toml
[[Users]]
Username = "alice"
Password = "change-me"
Role = "write"

[[Users]]
Username = "bob"
Password = "change-me-too"
Role = "read"
```

`write` users may create, modify, rename and delete under `files\` and edit
tags under `tags\`. `read` users get a read-only connection: the server
refuses their writes with `MEDIA_WRITE_PROTECTED` before they reach the
filesystem. Passwords are stored in plaintext because NTLMv2 needs them (or
their NT hash) server-side — keep the config file readable only by the
server's user (`chmod 600`).

### Ports

Port 445 is privileged. Options:

- systemd (recommended): `contrib/systemd/tie-smb.service` grants
  `AmbientCapabilities=CAP_NET_BIND_SERVICE` so the unprivileged `tie` user
  can bind 445.
- run as root (not recommended), or
- use a high port and tell clients: `ListenOn = ":4445"`, then
  `mount -t cifs //server:4445/tie mnt -o username=alice,port=4445` or
  `smbclient -p 4445 //server/tie -U alice`.

### systemd

```sh
sudo systemctl enable --now tie-smb
journalctl -u tie-smb -f
```

The unit runs as the `tie` user with the usual hardening
(`ProtectSystem=strict`, `PrivateTmp`, ...) and `PrivateTmp`, so the blob
cache and write-staging dirs live in a private `/tmp` that disappears with
the service. Shutdown is graceful: SIGINT/SIGTERM closes the server, every
open handle is closed (which commits its staged write), and the cache and
staging dirs are removed.

## Mounting from clients

### Linux (kernel CIFS client, `cifs-utils`)

```sh
sudo mount -t cifs //server/tie mnt -o username=alice,vers=3.0
sudo mount -t cifs //server:4445/tie mnt -o username=alice,port=4445   # high port
```

A high port needs **both** the `//server:port` suffix and `port=`. For
fstab-style persistence (credentials file, `uid=`/`gid=` for local
ownership):

```
//server/tie  /mnt/tie  cifs  credentials=/etc/tie/creds,vers=3.0,uid=1000,gid=1000  0  0
```

Without mounting, `smbclient` works like an FTP-ish shell:

```sh
smbclient -L //server -U alice                      # list shares
smbclient //server/tie -U alice -c 'ls; cd files'
```

### Windows (Explorer)

Address `\\server\tie` in Explorer's address bar, or from a command prompt:

```bat
net use W: \\server\tie /user:alice
```

Windows 10/11 negotiates SMB 3.0.2 automatically. If a mount is refused,
check that the server's dialect list (2.1–3.0.2) is acceptable to the
client's policy and that plain-NTLM/guest settings don't interfere — the
server authenticates with NTLMv2.

### macOS (Finder)

⌘K → `smb://server/tie` → Registered User → Connect. Or from a terminal:

```sh
mount_smbfs //alice@server/tie /Volumes/tie
```

## The share

One share (default name `tie`) mirrors the `--db` mount root:

| Path | Writable? | Contents |
|---|---|---|
| `query\` | no | `README`, `tags`, `types`, saved queries, ad-hoc tag-query directories |
| `files\` | yes | the path-based import tree: create, write, rename, mkdir, delete |
| `tags\` | yes | the same tree; each leaf is a text file holding that file's tags (one per line), each directory carries `.tags` (its own tags) and `.type` (its dir-type labels) |

### `files\` — the import tree

Browse and edit the path tree (`tie:/...`) as an ordinary share: create
directories, copy files in, edit them in place, rename, delete. Two
semantics worth knowing:

- **Writes commit at close.** The server stages the bytes while the file is
  open and uploads them (and rewrites the file's triples) when the client
  closes the handle. A client that drops the connection without closing
  still commits — the server closes every open handle on connection
  teardown.
- **Overwrites are versioned.** Copying new bytes onto an existing name
  supersedes the old content; the previous version moves into a
  `<filename>_prev` history directory according to the tie config's
  `PrevVersions` setting, exactly as with `tie import`.

Deleting a file unlinks it from that directory only; content that is
imported in several places stays visible in the others. Blob bytes stay in
the filehost — its retention policy governs them.

### `tags\` — editing tags and dir-types

Navigate to the file and edit its little text file — one tag per line, save
replaces the set (empty file clears it):

```sh
echo "jazz
mellow" > "tags/music/jazz trio/live set.mp3"
```

Every directory carries two control files: `.tags` (the directory's own
tags) and `.type` (its dir-type classification labels, e.g. `audio-dir`,
or custom labels like `live-album`). A real child file literally named
`.tags`/`.type` is shadowed by the control file — that is why the control
files use dotfile names.

### `query\` — tag queries (read-only)

A directory under `query\` whose name is a query lists the matching files;
open or copy them like any read-only file. Query syntax (space separates
terms, all ANDed; `-tag` excludes; `type:` scopes):

```
query\jazz                    files tagged jazz
query\jazz mellow             tagged jazz AND mellow
query\jazz mellow -live       jazz AND mellow, not live
query\mellow type:audio       audio files tagged mellow
query\type:image              all image files
query\type:live-album         dirs carrying the custom "live-album" label
```

`type:` accepts a friendly media name (`image`, `audio`, `video`,
`document`, `archive`), a full tie-type name (`audio-file`), or a custom
dir-type label. Helper files: `query\README` (the same help, in-file),
`query\tags` (every known tag), `query\types` (every known dir-type label).
Saved queries from the tie config's `[Queries]` table appear as
ready-made directories (`chill-jazz = jazz mellow -live` gives
`query\chill-jazz`).

Archive blobs (zips) appear as directories of their members, read-only,
under both `query\` and `files\`. Nested member paths are flattened to base
names; collisions get a `~<n>` suffix.

## Name escaping (read this first)

tie names are arbitrary strings; SMB is not. Every name the share lists is
therefore **escaped** into an SMB-legal display name, and unescaped again
when a client asks for it, so nothing is hidden and every name round-trips:

- each byte in `\ / : * ? " < > |`, control bytes (< 0x20, 0x7f), and `~`
  itself → `~XX` (lowercase hex): `a/b` → `a~2fb`, `type:audio` →
  `type~3aaudio`, `~x` → `~7ex`;
- a trailing `.` or space → `~2e` / `~20` (SMB refuses trailing dot/space);
- Win32 reserved device names get their first letter escaped:
  `CON.txt` → `~43ON.txt`.

So a name containing `:` (like the query `type:audio`) is reachable — as
`type~3aaudio` — even though the SMB transport itself refuses raw colons
(they would read as alternate-data-stream syntax). Names that need no
escaping are shown unchanged. The escape is reversible and collision-free:
create a file named `we~3aird~2aname.txt` and it stays exactly reachable,
because a literal `~` in a tie name escapes to `~7e`.

## Case

tie names are case-sensitive. The server tries the name exactly as typed
first; only when that misses does it retry case-insensitively (the smb
library's case-fold fallback). If two names differ only by case (`README`
and `readme`), the exact spelling always wins, and a case-folded guess
picks the first sorted match.

## Timestamps

The go-filesystems interface carries no timestamps, so the server reports
"now" for all four FILETIMEs. tie's import date (`TagDate`) is not surfaced
over SMB. Timestamp/attribute SET_INFO requests are accepted and dropped,
so copy tools don't fail.

## Security and roles

- Authentication is NTLMv2 (via SPNEGO); sessions are signed (SMB 3.0.2
  HMAC-SHA256). **The transport does not encrypt** — run it on a trusted
  LAN, or tunnel it (WireGuard etc.). Firewall the port accordingly.
- Config holds plaintext passwords: `chmod 600`, owned by the server user.
- Role enforcement is per connection: read-only users never reach the
  filesystem's write paths.
- The server needs the same triplestore/filehost credentials as the tie
  CLI — put them in the tie client config (`TieConfig`), not in the SMB
  accounts.

## Limits (v1)

- No SMB 3.1.1, no encryption (pre-3.1.1 dialects sign but do not
  encrypt), no oplocks/leases, no alternate data streams (paths with `:`
  are refused by the transport — use the escaped spelling), no
  lwssvc/lsarpc pipes (srvsvc share enumeration works).
- No timestamps (see above).
- Writes commit when the client closes the handle; there is no
  server-visible flush semantics beyond that.
- Renames always replace an existing destination file (the SMB library
  ignores the client's ReplaceIfExists flag); replacing a directory is
  refused.
- Deleting a non-empty directory fails (closest SMB status: name
  collision).
- Cross-view renames (`files\` ↔ `tags\` ↔ `query\`) are refused; renames
  stay inside `files\`.
- The share reports a fixed, synthetic capacity — ignore the free-space
  numbers clients show.

## Troubleshooting

| Symptom | Meaning |
|---|---|
| `STATUS_LOGON_FAILURE` | Wrong username/password (check the `[[Users]]` tables; note the TOML section-ordering gotcha). |
| `STATUS_MEDIA_WRITE_PROTECTED` | The account has `Role = "read"`, or you are writing into `query\`. |
| `STATUS_OBJECT_NAME_NOT_FOUND` on a name you can see listed | The name needs escaping — use the `~XX` spelling the listing shows (a raw `:` in the path is refused by the transport). |
| `STATUS_OBJECT_NAME_COLLISION` | Name exists (create), or you are deleting a non-empty directory. |
| `STATUS_ACCESS_DENIED` on a write inside `files\` | The target is a read-only object (query result, archive member, README...). |
| `mount -t cifs` says "Host is down" / protocol error | Pin `vers=3.0`; for high ports pass both `//host:port` and `port=`. |
| Bind failure at start (`permission denied` on :445) | Needs root or `AmbientCapabilities=CAP_NET_BIND_SERVICE` (the systemd unit sets it). |
| Server refuses to start: "triplestore not reachable" | The `TieConfig` triplestore URL is wrong or the triplestore is down — tie-smb checks connectivity at startup. |

## Testing

`io/smbfs` carries unit tests for escaping and path resolution, live tests
against the test triplestore (skipped when it is down — see
`test-env/start.sh`), and an end-to-end integration test that speaks SMB to
an in-process server with
[`github.com/hirochachacha/go-smb2`](https://github.com/hirochachacha/go-smb2)
as a test-only client: mount, list, read, write, rename, delete, tag edit,
reader role.

```sh
go test ./io/smbfs/            # live parts skip when the test store is down
```

Manual smoke test against a running tie-smb on a high port:

```sh
smbclient -p 4445 //localhost/tie -U alice -c \
  'ls; cd files; mkdir smoke; cd smoke; put local.txt song.txt; get song.txt back.txt; rm song.txt; cd ..; rmdir smoke'
```
