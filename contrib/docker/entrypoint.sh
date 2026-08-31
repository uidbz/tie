#!/bin/sh
# Run tie-triplestore and tie-filehost together in one container.
#
# On the first run, if a config file is missing it is created from the baked-in
# example, pointed at the mounted /data volume and set to listen on all
# interfaces (TLS is expected to be terminated by a reverse proxy in front of
# the container). Mount your own /etc/tie to override.
set -eu

CONFDIR=/etc/tie
DATADIR=/data

mkdir -p "$DATADIR/db" "$DATADIR/data"

if [ ! -f "$CONFDIR/tie-triplestore.toml" ]; then
	echo "entrypoint: generating default $CONFDIR/tie-triplestore.toml"
	cat > "$CONFDIR/tie-triplestore.toml" <<-EOF
	ListenOn = "0.0.0.0:1161"
	Insecure = true
	DbPath = "$DATADIR/db"

	[[Users]]
	Username = "${TIE_USER:-defaultuser}"
	Password = "${TIE_PASSWORD:-defaultpassword}"
	EOF
fi

if [ ! -f "$CONFDIR/tie-filehost.toml" ]; then
	echo "entrypoint: generating default $CONFDIR/tie-filehost.toml"
	cat > "$CONFDIR/tie-filehost.toml" <<-EOF
	ListenOn = "0.0.0.0:1162"
	Insecure = true
	BlobPath = "$DATADIR/data"
	ReapInterval = "1h"
	EOF
fi

# Start both services, forwarding SIGTERM/SIGINT to both and exiting when
# either one dies (so the container restarts as a unit).
triplestore_pid=""
filehost_pid=""

shutdown() {
	echo "entrypoint: shutting down"
	[ -n "$triplestore_pid" ] && kill "$triplestore_pid" 2>/dev/null || true
	[ -n "$filehost_pid" ] && kill "$filehost_pid" 2>/dev/null || true
	wait 2>/dev/null || true
	exit 0
}
trap shutdown TERM INT

echo "entrypoint: starting tie-triplestore"
tie-triplestore -config "$CONFDIR/tie-triplestore.toml" &
triplestore_pid=$!

echo "entrypoint: starting tie-filehost"
tie-filehost -config "$CONFDIR/tie-filehost.toml" &
filehost_pid=$!

# Exit as soon as either process exits, then clean up the other.
wait -n "$triplestore_pid" "$filehost_pid"
echo "entrypoint: a service exited; stopping the container"
shutdown
