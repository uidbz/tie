#!/usr/bin/env bash
# Mount an immutable content-addressed directory by its tiedir hash.
# Usage: ./mount-hash.sh <dir-hash>
# Runs in the foreground; Ctrl-C to unmount.
set -euo pipefail
source "$(dirname "$0")/env.sh"

if [[ $# -lt 1 ]]; then
	echo "Usage: $0 <dir-hash>" >&2
	exit 1
fi

if mountpoint -q "$TIE_MNT" 2>/dev/null; then
	echo "$TIE_MNT is already a mountpoint. Unmount first (./stop.sh)." >&2
	exit 1
fi

echo "Mounting $1 at $TIE_MNT (Ctrl-C to unmount)"
cd "$TIE_ENV"
exec "$TIE_BIN/tie" -c "$TIE_CONFIG_NAME" mount "$1" "$TIE_MNT"
