#!/usr/bin/env bash
# Mount the live, tag-derived virtual filesystem at $TIE_MNT.
# Browse tag queries under $TIE_MNT/query/ and the path tree under
# $TIE_MNT/files/. Runs in the foreground; Ctrl-C to unmount. Re-tagging via
# ./seed.sh or `tie add` shows up without remounting.
set -euo pipefail
source "$(dirname "$0")/env.sh"

if mountpoint -q "$TIE_MNT" 2>/dev/null; then
	echo "$TIE_MNT is already a mountpoint. Unmount first (./stop.sh)." >&2
	exit 1
fi

echo "Mounting tag-derived filesystem at $TIE_MNT (Ctrl-C to unmount)"
echo "Try:  cat $TIE_MNT/query/tags  &&  ls $TIE_MNT/query/vacation/"
cd "$TIE_ENV"
exec "$TIE_BIN/tie" -c "$TIE_CONFIG_NAME" mount --db "$TIE_MNT"
