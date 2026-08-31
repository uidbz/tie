# Running tie as system services

`tie-triplestore` (triple store, port 1161) and `tie-filehost` (blob store, port
1162) are two separate services. This directory has everything to install them
as long-running services.

On Gentoo, prefer the ebuilds in [`gentoo/`](gentoo/README.md), which install
tie as a normal package (Portage-managed binaries, config, and service files).

## Quick install (systemd or OpenRC)

From a checkout of this repo:

```
sudo make install-server  # from the repo root
# or equivalently:
sudo ./contrib/install.sh
```

(`make install` on its own installs just the `tie` client, rootless, into your
`GOBIN` — use `install-server` for the triplestore and filehost.)

The installer:

- builds all three binaries (`tie`, `tie-triplestore`, `tie-filehost`) and installs
  them to `/usr/local/bin`,
- creates a dedicated `tie` system user,
- writes config to `/etc/tie/` (from the `*.toml.example` files, if not already
  present) and data dirs to `/var/lib/tie/`,
- detects systemd or OpenRC and installs + enables the matching service files.

Then edit `/etc/tie/tie-triplestore.toml` (at least the `[[Users]]` account) and
start the services:

```
# systemd
sudo systemctl start tie-triplestore tie-filehost

# OpenRC
sudo rc-service tie-triplestore start
sudo rc-service tie-filehost start
```

Existing config files are never overwritten, so re-running the installer to
upgrade binaries is safe.

## Layout

| Path                        | Purpose                                  |
|-----------------------------|------------------------------------------|
| `/usr/local/bin/tie-*`      | binaries                                 |
| `/etc/tie/*.toml`           | config (owned by `tie`, mode 0640)       |
| `/var/lib/tie/db`           | triplestore data                         |
| `/var/lib/tie/data`         | filehost content-addressed blobs         |

## Network exposure and TLS

The example configs default to plain HTTP (`Insecure = true`) with
`ListenOn = ":1161"` / `":1162"`. The intended setup is a personal media
library on a single PC or a small trusted LAN, so this works out of the box with
no certificate setup.

Note that `:1161` / `:1162` bind **all interfaces** (`0.0.0.0`), so the services
are reachable — unencrypted, with only HTTP basic auth — from every host on your
network. That is deliberate: it lets other machines on the LAN reach the library
without extra configuration. If you only want local access, change `ListenOn` to
`127.0.0.1:1161` / `127.0.0.1:1162`. Do not expose these ports to an untrusted
network or the public internet as-is.

To serve HTTPS directly, set `Insecure = false` and point `CertFile`/`KeyFile`
at a cert pair. Alternatively put a TLS-terminating reverse proxy (nginx /
Caddy) in front, keep `Insecure = true`, and bind `ListenOn` to localhost so
only the proxy reaches the service.

## Logging

Both services log to stderr, and under an init system that is all you need:

- **systemd** captures stderr into the journal — `journalctl -u tie-triplestore`
  (or `-u tie-filehost`). Because stderr is not a terminal, the output is plain
  `time=… level=INFO msg=…` text (no color escapes).
- **OpenRC** sends stderr to `/var/log/tie/${RC_SVCNAME}.log` (see the service
  scripts), again as plain text.

So leave `LogFile` empty in the TOML: the init system already persists the log
stream, and a second copy would be redundant.

`LogFile` is available if you want structured JSON written to a specific file,
but the systemd unit sandboxes writes to `/var/lib/tie` (`ProtectSystem=strict`
+ `ReadWritePaths`), so a `LogFile` outside that path is not writable — the
service still starts and falls back to stderr-only, but no file appears. Point
it under `/var/lib/tie` if you use it. (OpenRC runs unsandboxed, so any
`tie`-writable path works there.)

## Files

- `systemd/tie-triplestore.service`, `systemd/tie-filehost.service`
- `openrc/tie-triplestore`, `openrc/tie-filehost`
- `install.sh` — the installer
- `gentoo/` — Gentoo ebuilds (`net-misc/tie`, `acct-user/tie`,
  `acct-group/tie`) and install instructions
- `docker/entrypoint.sh` — used by the root `Dockerfile` to run both services
  in one container
- `backup/btrfs-send-backup.sh` — incremental filehost backup via BTRFS
  snapshot + send/receive to a remote BTRFS host
- `backup/rsync-backup.sh` — filehost backup via rsync mirror, for non-BTRFS
  hosts
- `backup/triplestore-db-backup.sh` — tie-triplestore (`DbPath`) backup via
  rsync; complements the logical `tie dump` path

## Filehost storage and backup

For multi-disk storage layout (BTRFS pooling, LVM, why not to bind-mount shard
ranges) and the backup scripts above, see
[`docs/filehost-storage-and-backup.md`](../docs/filehost-storage-and-backup.md).

## Docker

For a containerized deployment that runs both services together, use the
`Dockerfile` at the repo root:

```
docker build -t tie .
docker run -p 1161:1161 -p 1162:1162 -v tie-data:/data tie
```
