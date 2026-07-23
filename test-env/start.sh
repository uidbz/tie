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

# start_one runs a binary in the background from $TIE_ENV, so binaries that
# resolve config paths relative to the working dir (tie-daemon reads its -config
# as cwd/<name>) find their files. The working dir also holds the db/ and data/
# subdirs the services write into.
start_one() { # name binary pidfile logfile args...
	local name="$1" bin="$2" pidfile="$3" logfile="$4"; shift 4
	if is_running "$pidfile"; then
		echo "$name already running (pid $(cat "$pidfile"))"
		return
	fi
	echo "Starting $name ..."
	# Background a subshell that exec's the binary, so the process REPLACES the
	# subshell and $! (the subshell PID) is the binary's own PID. Backgrounding
	# the `cd && nohup ...` compound directly would record the subshell PID while
	# the binary ran at the next PID, leaving stop.sh unable to kill it.
	( cd "$TIE_ENV" && exec nohup "$bin" "$@" >"$logfile" 2>&1 ) &
	echo $! >"$pidfile"
	echo "  pid $(cat "$pidfile"), log $logfile"
}

# The daemon is configured by a TOML file (not flags). Generate it if missing so
# a fresh checkout works out of the box; an existing file (e.g. with extra users)
# is left untouched. DbPath is relative to $TIE_ENV, matching start_one's cwd.
if [[ ! -f "$TIE_DAEMON_CONFIG" ]]; then
	cat >"$TIE_DAEMON_CONFIG" <<-EOF
		ListenOn = ":1161"
		Insecure = true
		DbPath = "db"

		[[Users]]
		Username = "defaultuser"
		Password = "defaultpassword"
	EOF
fi

# The filehost is likewise configured by a TOML file (not flags). DbPath is
# relative to $TIE_ENV, matching start_one's cwd. ReapInterval = "0" disables
# the expired-blob reaper for the sandbox.
if [[ ! -f "$TIE_FILEHOST_CONFIG" ]]; then
	cat >"$TIE_FILEHOST_CONFIG" <<-EOF
		ListenOn = ":1162"
		Insecure = true
		DbPath = "data"
		ReapInterval = "0"
	EOF
fi

start_one "tie-daemon"   "$TIE_BIN/tie-daemon"   "$TIE_DAEMON_PID"   "$TIE_LOGS/daemon.log" \
	-config "$(basename "$TIE_DAEMON_CONFIG")"

start_one "tie-filehost" "$TIE_BIN/tie-filehost" "$TIE_FILEHOST_PID" "$TIE_LOGS/filehost.log" \
	-config "$(basename "$TIE_FILEHOST_CONFIG")"

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
