# Running tie as system services

`tie-daemon` (triple store, port 1161) and `tie-filehost` (blob store, port
1162) are two separate services. This directory has everything to install them
as long-running services.

## Quick install (systemd or OpenRC)

From a checkout of this repo:

```
sudo make install         # from the repo root
# or equivalently:
sudo ./contrib/install.sh
```

The installer:

- builds both binaries and installs them to `/usr/local/bin`,
- creates a dedicated `tie` system user,
- writes config to `/etc/tie/` (from the `*.toml.example` files, if not already
  present) and data dirs to `/var/lib/tie/`,
- detects systemd or OpenRC and installs + enables the matching service files.

Then edit `/etc/tie/tie-daemon.toml` (at least the `[[Users]]` account) and
start the services:

```
# systemd
sudo systemctl start tie-daemon tie-filehost

# OpenRC
sudo rc-service tie-daemon start
sudo rc-service tie-filehost start
```

Existing config files are never overwritten, so re-running the installer to
upgrade binaries is safe.

## Layout

| Path                        | Purpose                                  |
|-----------------------------|------------------------------------------|
| `/usr/local/bin/tie-*`      | binaries                                 |
| `/etc/tie/*.toml`           | config (owned by `tie`, mode 0640)       |
| `/var/lib/tie/db`           | daemon triple-store data                 |
| `/var/lib/tie/data`         | filehost content-addressed blobs         |

## TLS

The service files run the daemons as configured in their TOML. TLS is normally
terminated by a reverse proxy (nginx / Caddy) in front of them: run with
`Insecure = true` and bind `ListenOn` to localhost, and let the proxy handle
certificates. Alternatively set `CertFile`/`KeyFile` in the config to serve
HTTPS directly.

## Files

- `systemd/tie-daemon.service`, `systemd/tie-filehost.service`
- `openrc/tie-daemon`, `openrc/tie-filehost`
- `install.sh` — the installer
- `docker/entrypoint.sh` — used by the root `Dockerfile` to run both services
  in one container
- `backup/btrfs-send-backup.sh` — incremental filehost backup via BTRFS
  snapshot + send/receive to a remote BTRFS host
- `backup/rsync-backup.sh` — filehost backup via rsync mirror, for non-BTRFS
  hosts
- `backup/daemon-db-backup.sh` — tie-daemon triple-store (`DbPath`) backup via
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
