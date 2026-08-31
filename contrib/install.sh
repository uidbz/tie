#!/usr/bin/env bash
# Install the tie client, tie-triplestore, and tie-filehost, wiring up the
# triplestore and filehost as system services.
#
# Builds all three binaries, installs them to /usr/local/bin, creates a
# dedicated `tie` system user, lays down config in /etc/tie and data dirs in
# /var/lib/tie, then installs service files for whichever init system is
# detected (systemd or OpenRC). The client (tie) is installed too so a server
# box has the CLI on hand.
#
# Usage: sudo ./contrib/install.sh
#
# Re-running is safe: existing config files are never overwritten.
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
BINDIR="$PREFIX/bin"
CONFDIR="/etc/tie"
DATADIR="/var/lib/tie"
TIE_USER="tie"

# Resolve repo root from this script's location (contrib/ -> repo root).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo $0)"
command -v go >/dev/null 2>&1 || die "go toolchain not found in PATH"

# --- Detect init system --------------------------------------------------
INIT=""
if [ -d /run/systemd/system ]; then
	INIT="systemd"
elif command -v rc-update >/dev/null 2>&1; then
	INIT="openrc"
else
	warn "no supported init system detected; binaries and config will be installed, but no service will be set up"
fi
log "init system: ${INIT:-none}"

# --- Build --------------------------------------------------------------
log "building binaries (static)"
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -buildvcs=false -o "$SCRIPT_DIR/tie" ./cmd/tie )
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -buildvcs=false -o "$SCRIPT_DIR/tie-triplestore" ./cmd/tie-triplestore )
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -buildvcs=false -o "$SCRIPT_DIR/tie-filehost" ./cmd/tie-filehost )

# --- User ---------------------------------------------------------------
if ! id "$TIE_USER" >/dev/null 2>&1; then
	log "creating system user '$TIE_USER'"
	if command -v useradd >/dev/null 2>&1; then
		useradd --system --home-dir "$DATADIR" --shell /usr/sbin/nologin "$TIE_USER"
	elif command -v adduser >/dev/null 2>&1; then
		# busybox/alpine adduser
		adduser -S -H -h "$DATADIR" -s /sbin/nologin "$TIE_USER"
	else
		die "no useradd/adduser found to create the '$TIE_USER' user"
	fi
else
	log "system user '$TIE_USER' already exists"
fi

# --- Binaries -----------------------------------------------------------
log "installing binaries to $BINDIR"
install -d "$BINDIR"
install -m 0755 "$SCRIPT_DIR/tie" "$BINDIR/tie"
install -m 0755 "$SCRIPT_DIR/tie-triplestore" "$BINDIR/tie-triplestore"
install -m 0755 "$SCRIPT_DIR/tie-filehost" "$BINDIR/tie-filehost"
rm -f "$SCRIPT_DIR/tie" "$SCRIPT_DIR/tie-triplestore" "$SCRIPT_DIR/tie-filehost"

# --- Data dirs ----------------------------------------------------------
log "creating data dirs under $DATADIR"
install -d -o "$TIE_USER" -g "$TIE_USER" -m 0755 "$DATADIR" "$DATADIR/db" "$DATADIR/data"

# --- Config -------------------------------------------------------------
install -d -m 0755 "$CONFDIR"

# install_config <example-src> <dest> <path-key> <path-value>
# Copies the example (once), pointing the named path key at the installed data
# dir. Config holds credentials, so it is chmod 0640 and owned by the tie user.
install_config() {
	local src="$1" dest="$2" key="$3" val="$4"
	if [ -e "$dest" ]; then
		log "keeping existing $dest"
		return
	fi
	log "installing $dest"
	sed "s#^${key} = .*#${key} = \"${val}\"#" "$src" > "$dest"
	chown "$TIE_USER:$TIE_USER" "$dest"
	chmod 0640 "$dest"
}

install_config "$REPO_ROOT/cmd/tie-triplestore/tie-triplestore.toml.example" \
	"$CONFDIR/tie-triplestore.toml" "DbPath" "$DATADIR/db"
install_config "$REPO_ROOT/cmd/tie-filehost/tie-filehost.toml.example" \
	"$CONFDIR/tie-filehost.toml" "BlobPath" "$DATADIR/data"

# --- Service files ------------------------------------------------------
case "$INIT" in
systemd)
	log "installing systemd units"
	install -m 0644 "$SCRIPT_DIR/systemd/tie-triplestore.service" /etc/systemd/system/tie-triplestore.service
	install -m 0644 "$SCRIPT_DIR/systemd/tie-filehost.service" /etc/systemd/system/tie-filehost.service
	systemctl daemon-reload
	log "enabling services"
	systemctl enable tie-triplestore.service tie-filehost.service
	cat <<-EOF

	Done. Edit config in $CONFDIR (especially [[Users]] in tie-triplestore.toml), then:
	    systemctl start tie-triplestore tie-filehost
	    systemctl status tie-triplestore tie-filehost
	EOF
	;;
openrc)
	log "installing OpenRC service scripts"
	install -m 0755 "$SCRIPT_DIR/openrc/tie-triplestore" /etc/init.d/tie-triplestore
	install -m 0755 "$SCRIPT_DIR/openrc/tie-filehost" /etc/init.d/tie-filehost
	log "adding services to the default runlevel"
	rc-update add tie-triplestore default
	rc-update add tie-filehost default
	cat <<-EOF

	Done. Edit config in $CONFDIR (especially [[Users]] in tie-triplestore.toml), then:
	    rc-service tie-triplestore start
	    rc-service tie-filehost start
	EOF
	;;
*)
	cat <<-EOF

	Binaries and config installed. Start them manually, e.g.:
	    $BINDIR/tie-triplestore   -config $CONFDIR/tie-triplestore.toml
	    $BINDIR/tie-filehost -config $CONFDIR/tie-filehost.toml
	EOF
	;;
esac
