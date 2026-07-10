#!/usr/bin/env bash
# Start the tie-daemon (triple store, :1161) and tie-filehost (blob store, :1162)
# in the background, both in insecure HTTP mode. Idempotent-ish: refuses to start
# if a PID file points at a live process.
set -euo pipefail
source "$(dirname "$0")/env.sh"

if [[ ! -x "$TIE_BIN/tie-daemon" || ! -x "$TIE_BIN/tie-filehost" ]]; then
	echo "Binaries missing. Run ./build.sh first." >&2
	exit 1
fi

mkdir -p "$TIE_DB_PATH" "$TIE_DATA_PATH" "$TIE_MNT" "$TIE_LOGS"

is_running() { # $1 = pidfile
	[[ -f "$1" ]] && kill -0 "$(cat "$1")" 2>/dev/null
}

start_one() { # name binary pidfile logfile args...
	local name="$1" bin="$2" pidfile="$3" logfile="$4"; shift 4
	if is_running "$pidfile"; then
		echo "$name already running (pid $(cat "$pidfile"))"
		return
	fi
	echo "Starting $name ..."
	nohup "$bin" "$@" >"$logfile" 2>&1 &
	echo $! >"$pidfile"
	echo "  pid $(cat "$pidfile"), log $logfile"
}

start_one "tie-daemon"   "$TIE_BIN/tie-daemon"   "$TIE_DAEMON_PID"   "$TIE_LOGS/daemon.log" \
	--insecure --listen ":1161" --db-path "$TIE_DB_PATH"

start_one "tie-filehost" "$TIE_BIN/tie-filehost" "$TIE_FILEHOST_PID" "$TIE_LOGS/filehost.log" \
	--insecure --listen ":1162" --path "$TIE_DATA_PATH"

echo "Waiting for services to accept connections ..."
for i in $(seq 1 30); do
	if curl -s -o /dev/null "http://localhost:1161/" && \
	   curl -s -o /dev/null "http://localhost:1162/"; then
		echo "Both services are up."
		echo "  webservice: $TIE_WEBSERVICE"
		echo "  filehost:   $TIE_FILEHOST_URL"
		exit 0
	fi
	sleep 0.2
done
echo "Services did not become ready in time; check $TIE_LOGS/*.log" >&2
exit 1
