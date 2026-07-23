#!/usr/bin/env bash
# Mirror a tie-filehost blob store to a remote host with rsync, for hosts
# without BTRFS send/receive.
#
# The store is content-addressed and immutable: existing blobs never change and
# new blobs are new files. So --size-only is safe (the content hash is the
# checksum) and lets rsync skip per-file checksumming, copying only new blobs.
#
# By default this mirrors deletions (--delete) so the backup is a faithful DR
# copy including reaper-expired blobs. For an archival backup that retains what
# the primary reaped, set TIE_MIRROR_DELETE=0 (see docs).
#
# See docs/filehost-storage-and-backup.md for the full rationale.
set -euo pipefail

# ---- Configuration (override via environment) -------------------------------
# Local blob store root (tie-filehost DbPath). Trailing slash matters to rsync;
# the script normalizes it below.
SRC="${TIE_FILEHOST_DATA:-/mnt/filehost}"
# Remote target: user@host:/path the blobs are mirrored into.
REMOTE="${TIE_BACKUP_REMOTE:-backup@backup-host}"
REMOTE_DIR="${TIE_BACKUP_REMOTE_DIR:-/mnt/filehost-backup}"
# ssh command (add -i / -p here if needed).
SSH="${TIE_BACKUP_SSH:-ssh}"
# Mirror deletions (1) for a DR copy, or retain everything (0) for an archive.
MIRROR_DELETE="${TIE_MIRROR_DELETE:-1}"
# -----------------------------------------------------------------------------

# Normalize to a single trailing slash so rsync copies contents, not the dir.
src="${SRC%/}/"

opts=(-a --size-only --partial --human-readable --info=stats1)
if [[ "$MIRROR_DELETE" == "1" ]]; then
	opts+=(--delete --delete-delay)
fi

echo "==> Ensuring remote dir exists: $REMOTE:$REMOTE_DIR"
$SSH "$REMOTE" "mkdir -p '$REMOTE_DIR'"

echo "==> rsync ${opts[*]} $src -> $REMOTE:$REMOTE_DIR/"
rsync "${opts[@]}" -e "$SSH" "$src" "$REMOTE:$REMOTE_DIR/"

echo "==> Done."
