#!/usr/bin/env bash
# Incrementally back up a BTRFS tie-filehost blob store to a remote BTRFS host
# via read-only snapshots and btrfs send/receive.
#
# The filehost store is content-addressed and immutable, so a read-only snapshot
# is always internally consistent and each incremental send ships only the blobs
# added (or, if the reaper runs, removed) since the previous snapshot.
#
# Requires: the blob store lives on its own BTRFS subvolume ($SUBVOL), and the
# remote host can `btrfs receive` into $REMOTE_DIR (run as root, or grant the
# CAP_SYS_ADMIN / passwordless-sudo needed for receive).
#
# See docs/filehost-storage-and-backup.md for the full rationale.
set -euo pipefail

# ---- Configuration (override via environment) -------------------------------
# The BTRFS subvolume holding the blob store (tie-filehost DbPath, or its parent
# subvolume). Must be a subvolume, not a plain directory.
SUBVOL="${TIE_FILEHOST_SUBVOL:-/mnt/filehost}"
# Local directory holding read-only snapshots. Must be on the same BTRFS FS.
SNAP_DIR="${TIE_SNAP_DIR:-/mnt/filehost/.snapshots}"
# Remote target: user@host and the directory it will `btrfs receive` into.
REMOTE="${TIE_BACKUP_REMOTE:-backup@backup-host}"
REMOTE_DIR="${TIE_BACKUP_REMOTE_DIR:-/mnt/filehost-backup/.snapshots}"
# ssh command (add -i / -p here if needed).
SSH="${TIE_BACKUP_SSH:-ssh}"
# How many local snapshots to keep (remote keeps its own; see docs).
KEEP="${TIE_SNAP_KEEP:-7}"
# Timestamp for the new snapshot. Pass one in for reproducible/testable runs;
# defaults to now.
STAMP="${TIE_SNAP_STAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
# -----------------------------------------------------------------------------

new_snap="$SNAP_DIR/$STAMP"

mkdir -p "$SNAP_DIR"

# The previous local snapshot, if any, is the parent for an incremental send.
prev_snap="$(find "$SNAP_DIR" -mindepth 1 -maxdepth 1 -type d 2>/dev/null \
	| sort | tail -n1 || true)"

echo "==> Creating read-only snapshot: $new_snap"
btrfs subvolume snapshot -r "$SUBVOL" "$new_snap"
# Ensure the snapshot is fully on disk before sending it.
sync

echo "==> Ensuring remote receive dir exists: $REMOTE:$REMOTE_DIR"
$SSH "$REMOTE" "mkdir -p '$REMOTE_DIR'"

if [[ -n "$prev_snap" && "$prev_snap" != "$new_snap" ]]; then
	echo "==> Incremental send (parent: $(basename "$prev_snap"))"
	btrfs send -p "$prev_snap" "$new_snap" \
		| $SSH "$REMOTE" "btrfs receive '$REMOTE_DIR'"
else
	echo "==> No prior snapshot; full send"
	btrfs send "$new_snap" \
		| $SSH "$REMOTE" "btrfs receive '$REMOTE_DIR'"
fi

echo "==> Pruning local snapshots, keeping newest $KEEP"
# Delete oldest-first, leaving $KEEP most recent. btrfs subvolume delete (not
# rm) is required to remove a snapshot subvolume.
mapfile -t snaps < <(find "$SNAP_DIR" -mindepth 1 -maxdepth 1 -type d | sort)
count=${#snaps[@]}
if (( count > KEEP )); then
	for ((i = 0; i < count - KEEP; i++)); do
		echo "    deleting $(basename "${snaps[i]}")"
		btrfs subvolume delete "${snaps[i]}"
	done
fi

echo "==> Done: $STAMP sent to $REMOTE:$REMOTE_DIR"
