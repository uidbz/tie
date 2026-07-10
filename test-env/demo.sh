#!/usr/bin/env bash
# End-to-end demo: build, start, seed, mount, browse, and tear down.
# Non-interactive: it mounts in the background, runs a few ls/cat commands
# against the tag-derived filesystem, then unmounts and stops the services.
set -euo pipefail
source "$(dirname "$0")/env.sh"

cleanup() {
	echo
	echo "== Cleaning up =="
	if mountpoint -q "$TIE_MNT" 2>/dev/null; then
		fusermount -u "$TIE_MNT" 2>/dev/null || fusermount3 -u "$TIE_MNT" 2>/dev/null || true
	fi
	"$TIE_ENV/stop.sh" || true
}
trap cleanup EXIT

"$TIE_ENV/build.sh"
"$TIE_ENV/start.sh"
"$TIE_ENV/seed.sh"

echo
echo "== Mounting tag-derived FS in background =="
( cd "$TIE_ENV" && "$TIE_BIN/tie" -c "$TIE_CONFIG_NAME" mount --db "$TIE_MNT" ) >"$TIE_LOGS/mount.log" 2>&1 &
MOUNT_PID=$!

# wait for the mount to appear
for i in $(seq 1 30); do
	mountpoint -q "$TIE_MNT" 2>/dev/null && break
	sleep 0.2
done

echo
echo "== Browsing $TIE_MNT =="
set -x
ls "$TIE_MNT"
ls "$TIE_MNT/by-tag"
ls -la "$TIE_MNT/by-tag/vacation"
cat "$TIE_MNT/by-tag/vacation/beach.txt"
ls "$TIE_MNT/by-tag/vacation/holiday-pics"
cat "$TIE_MNT/by-tag/vacation/holiday-pics/pic1.txt"
ls "$TIE_MNT/by-tag/outdoors"
set +x

echo
echo "== Demo complete; unmounting =="
fusermount -u "$TIE_MNT" 2>/dev/null || fusermount3 -u "$TIE_MNT" 2>/dev/null || true
wait "$MOUNT_PID" 2>/dev/null || true
