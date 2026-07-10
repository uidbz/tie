#!/usr/bin/env bash
# Stop the daemon and filehost, and unmount the FUSE mount if present.
set -uo pipefail
source "$(dirname "$0")/env.sh"

# Unmount first so the filehost isn't pulled out from under a live mount.
if mountpoint -q "$TIE_MNT" 2>/dev/null; then
	echo "Unmounting $TIE_MNT ..."
	fusermount -u "$TIE_MNT" 2>/dev/null || fusermount3 -u "$TIE_MNT" 2>/dev/null || true
fi

stop_one() { # name pidfile
	local name="$1" pidfile="$2"
	if [[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
		echo "Stopping $name (pid $(cat "$pidfile")) ..."
		kill "$(cat "$pidfile")" 2>/dev/null || true
		wait "$(cat "$pidfile")" 2>/dev/null || true
	else
		echo "$name not running"
	fi
	rm -f "$pidfile"
}

stop_one "tie-filehost" "$TIE_FILEHOST_PID"
stop_one "tie-daemon"   "$TIE_DAEMON_PID"
echo "Done."
