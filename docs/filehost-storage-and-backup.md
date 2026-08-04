# Running tie-filehost: disk layout and backup

This guide covers how to lay out storage for a `tie-filehost` deployment and how
to back it up, plus a companion section on backing up the `tie-daemon`
triple-store. It is written for a production host, not the `test-env` sandbox.

Two facts about the filehost drive everything here:

- **Blobs are immutable and content-addressed.** A blob's path is derived from a
  HighwayHash of its bytes (`cmd/tie-filehost/main.go`, `PathFromHash`). The same
  content always lands at the same path, and a path's contents never change. So a
  backup only ever gains new blobs and (if the reaper runs) loses expired ones —
  it never has to reconcile in-place edits.
- **The store is one root, sharded by count, not by data.** Every blob lives
  under `BlobPath` as `<xx>/<yy>/<hash>` — two levels of 2-hex directories, up to
  256×256 leaf dirs. The prefix comes from the content hash, so blobs distribute
  roughly uniformly *by object count* across shards, regardless of their size.

## Disk layout

### Don't bind-mount shard ranges

A tempting layout is to mount shard prefixes onto different disks
(`00,01→disk1`, `03,04→disk2`, …). Avoid it:

- Disks of different sizes force you to hand-tune which prefixes go where, and
  re-tune whenever one fills. Blobs distribute by *count*, not size, so an even
  prefix split does not mean an even byte split.
- `MakeDestinationPath` calls `MkdirAll` on demand and the reaper's
  `pruneShardDirs` *removes* now-empty shard directories. A shard dir that is
  actually a mountpoint turns those routine operations into failure cases.
- It is 256 mount decisions to babysit for a problem the filesystem already
  solves.

### BTRFS hosts: pool the disks into one filesystem

On BTRFS, let the filesystem pool mismatched disks and point `BlobPath` at the one
mount. The filehost keeps its single-root model untouched.

```bash
# New pool across three odd-sized disks:
mkfs.btrfs -d single -m raid1 /dev/sdb /dev/sdc /dev/sdd
mount /dev/sdb /mnt/filehost     # any member device mounts the whole pool

# Or grow an existing pool later:
btrfs device add /dev/sdX /mnt/filehost
btrfs balance start /mnt/filehost
```

- `-d single` uses the **full summed capacity** of the disks and spreads data
  chunks across them automatically. `-m raid1` keeps two copies of *metadata*
  (cheap) so a single-disk metadata corruption doesn't sink the whole FS.
- Tradeoff: with `-d single`, losing one disk loses the blobs stored on it.
  That is acceptable when a second server holds the backup (below). If you want
  local redundancy instead, use `-d raid1` — it tolerates one disk death and
  handles mixed sizes fine as long as the largest disk ≤ sum of the rest, at the
  cost of ~half usable capacity.

**Recommendation:** one BTRFS pool, `-d single`, durability delegated to the
backup server.

### Non-BTRFS hosts

You will also run the filehost on hosts without BTRFS. Two good options:

1. **Single large disk / hardware or mdadm RAID.** Simplest: put `BlobPath` on one
   filesystem and let RAID (or the cloud provider's block store) handle disk
   failure. Nothing filehost-specific to configure.
2. **LVM to pool mismatched disks.** If you have several odd-sized disks and no
   BTRFS, LVM gives you the same "one big filesystem" result:

   ```bash
   pvcreate /dev/sdb /dev/sdc /dev/sdd
   vgcreate filehost /dev/sdb /dev/sdc /dev/sdd
   lvcreate -l 100%FREE -n blobs filehost
   mkfs.ext4 /dev/filehost/blobs
   mount /dev/filehost/blobs /mnt/filehost
   ```

   Point `BlobPath` at the mount. Grow later with `vgextend` + `lvextend -r`.

The rule is the same on every platform: **give the filehost one root directory
and pool underneath it.** Never split the shard tree across mounts by hand.

## Backup

### BTRFS primary → BTRFS backup server: snapshot + send/receive

When both the primary and the backup server run BTRFS, incremental
`btrfs send | btrfs receive` is the right transport. It walks filesystem metadata
to ship only the blocks that changed since the previous snapshot, so it never
scans the 65k-directory shard tree or re-stats every file — a decisive win once
the store is large.

Because blobs are immutable, a read-only snapshot is always internally
consistent; the only transient file is the `tempfile` an in-flight upload writes
before rename, and a snapshot simply may or may not include it — either way the
committed blobs are intact.

Use `contrib/backup/btrfs-send-backup.sh` (below). Run it from cron:

```cron
# Every night at 02:30, snapshot the filehost and send to the backup server.
30 2 * * *  /opt/tie/contrib/backup/btrfs-send-backup.sh >> /var/log/tie-backup.log 2>&1
```

### Non-BTRFS primary: rsync mirror

Without `send/receive`, mirror the blob tree with `rsync` over SSH. Content
addressing makes this efficient and safe:

- New blobs are new files; `rsync` copies only those.
- Existing blobs never change, so `--size-only` is safe and skips per-file
  checksumming (the content hash *is* the checksum). This avoids re-reading every
  file on the source each run.

Use `contrib/backup/rsync-backup.sh` (below), also cron-friendly.

### The reaper interacts with backups

`ReapInterval` defaults to `"1h"`: the primary deletes expired blobs on a timer
(`cmd/tie-filehost/main.go`). Decide what the backup should do with deletions:

- **Backup mirrors the primary exactly** (disaster recovery only): let deletions
  propagate. `btrfs send` carries them automatically; the rsync script uses
  `--delete`. The backup is a faithful copy, expired blobs and all.
- **Backup retains everything the primary reaped** (archival): do *not* let
  deletions through. On BTRFS, keep the received read-only snapshots around
  forever — each is a point-in-time floor no later reap can rewind. With rsync,
  drop `--delete`. Alternatively run the primary with `ReapInterval="0"` (reaper
  off) and handle retention elsewhere.

Pick one deliberately; the default rsync/send behavior below mirrors deletions,
which is the safe assumption for a pure DR backup.

### Optional hot standby

Having the *bytes* on the backup server is independent from being able to
*serve* them. If you also want failover serving, run a second `tie-filehost` on
the backup host pointed at the received/mirrored blob root. Keep that as a
separate decision — the backup's job is durability first.

## Daemon triple-store (a separate backup concern)

The filehost holds the *bytes*; the `tie-daemon` triple-store holds everything
*about* them — filenames, tags, the virtual directory tree, relations. Losing it
loses all of that even if every blob survives. Back it up separately.

The daemon's on-disk model differs from the filehost's in a way that changes the
backup strategy:

- Its data lives under `DbPath` as `<namespace>/<collection>.tie` — one file per
  collection, made of fixed-width records the daemon **appends to and rewrites
  in place** (`tiedb/tietree.go`, `tiedb/filehandling.go`). These files are
  *mutable*, so the filehost's `rsync --size-only` shortcut is **not** safe here:
  a file's contents can change at record boundaries without a size delta.
- On load the daemon trims a partial trailing record left by a crash mid-write
  (`openDB` in `filehandling.go`), and it `Sync()`s on idle close. So a copy
  taken while the daemon runs is still *openable* — but not guaranteed to be a
  clean point-in-time image.

You have three options, in increasing order of guarantee:

1. **Logical dump (most portable).** `tie dump` emits the whole collection as
   TSV and `tie restore` reads it back; `test-env/backup.sh` wraps both. This is
   engine-independent — it survives on-disk-format changes and is trivial to
   inspect or diff — but it is a full logical export each run, not incremental.
   Best for periodic archival snapshots and for migrating between daemon
   versions.
2. **File-level mirror while running.** `contrib/backup/daemon-db-backup.sh`
   rsyncs `DbPath`. It deliberately does *not* use `--size-only`; it compares by
   mtime+size, or by full checksum with `TIE_DB_CHECKSUM=1`. Good enough for a DR
   copy given the crash-trimming behavior above, but the image is not strictly
   point-in-time.
3. **Filesystem snapshot (strongest, online).** If `DbPath` is on BTRFS or LVM,
   snapshot it and back up the snapshot — the snapshot is crash-consistent, and
   the daemon's partial-record trimming makes that consistent state loadable.
   This is the same approach the filehost uses; you can extend
   `btrfs-send-backup.sh` to a second subvolume for `DbPath`. For an offline,
   perfectly clean image, stop the daemon briefly and copy `DbPath`.

Recommendation: run the **logical dump** on a schedule for portability and
easy inspection, and additionally take a **filesystem snapshot** (or the
file-level mirror on non-snapshotting hosts) for fast full-state recovery.

```cron
# Nightly logical dump, kept alongside other backups.
0 3 * * *  cd /opt/tie/test-env && ./backup.sh dump /var/backups/tie/db-$(date -u +\%Y\%m\%d).tsv
# Nightly file-level mirror of DbPath to the backup host (non-snapshot hosts).
15 3 * * * /opt/tie/contrib/backup/daemon-db-backup.sh >> /var/log/tie-db-backup.log 2>&1
```

Note `test-env/backup.sh` is wired to the sandbox's `config.toml`; on a
production host, run `tie -c /etc/tie/... dump` directly (or adapt the script's
paths) rather than using the test-env wrapper as-is.
