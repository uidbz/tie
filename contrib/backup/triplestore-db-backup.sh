#!/usr/bin/env bash
# Back up a tie-triplestore triple-store (its DbPath directory) to a remote host.
#
# Unlike the filehost, the triplestore's on-disk files are MUTABLE: each
# collection is a "<namespace>/<collection>.tie" file of fixed-width records that
# the server appends to and rewrites in place. So --size-only is NOT safe here (a
# file can grow or change without a size delta at record boundaries); this script
# lets rsync decide by mtime+size (default) or full checksum (TIE_DB_CHECKSUM=1).
#
# The server trims any partial trailing record on load, so a copy taken while the
# server is running is still openable — but for a guaranteed point-in-time image
# prefer a filesystem snapshot (BTRFS/LVM) of DbPath, or stop the server briefly.
# For a portable, engine-independent backup, use the logical `tie dump` path
# instead (see docs/filehost-storage-and-backup.md, "Triple-store").
#
# See docs/filehost-storage-and-backup.md for the full rationale.
set -euo pipefail

# ---- Configuration (override via environment) -------------------------------
# Local triplestore DbPath (directory of <namespace>/<collection>.tie files).
SRC="${TIE_TRIPLESTORE_DB:-/var/lib/tie/db}"
# Remote target: user@host:/path the DB is mirrored into.
REMOTE="${TIE_BACKUP_REMOTE:-backup@backup-host}"
REMOTE_DIR="${TIE_BACKUP_REMOTE_DIR:-/var/lib/tie-backup/db}"
# ssh command (add -i / -p here if needed).
SSH="${TIE_BACKUP_SSH:-ssh}"
# 1 = full-checksum comparison (safest, re-reads every file); 0 = mtime+size.
CHECKSUM="${TIE_DB_CHECKSUM:-0}"
# Mirror deletions (1) so a dropped collection disappears from the backup too.
MIRROR_DELETE="${TIE_MIRROR_DELETE:-1}"
# -----------------------------------------------------------------------------

# Normalize to a single trailing slash so rsync copies contents, not the dir.
src="${SRC%/}/"

opts=(-a --partial --human-readable --info=stats1)
[[ "$CHECKSUM" == "1" ]] && opts+=(--checksum)
[[ "$MIRROR_DELETE" == "1" ]] && opts+=(--delete --delete-delay)

echo "==> Ensuring remote dir exists: $REMOTE:$REMOTE_DIR"
$SSH "$REMOTE" "mkdir -p '$REMOTE_DIR'"

echo "==> rsync ${opts[*]} $src -> $REMOTE:$REMOTE_DIR/"
rsync "${opts[@]}" -e "$SSH" "$src" "$REMOTE:$REMOTE_DIR/"

echo "==> Done."
