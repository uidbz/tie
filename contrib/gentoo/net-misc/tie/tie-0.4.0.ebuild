# Copyright 2023-2026 Johan Straarup
# Distributed under the terms of the BSD 3-Clause License

EAPI=8

inherit go-module systemd

DESCRIPTION="Triple-store client with a content-addressed filehost and FUSE mount"
HOMEPAGE="https://sr.ht/~uid/tie"

# Two source artifacts:
#   1. the release source archive from sourcehut, and
#   2. a dependency tarball with every Go module the build needs, so the build
#      runs fully offline inside Portage's network sandbox (the ::gentoo way).
#
# Regenerate the deps tarball for a new release with:
#     contrib/gentoo/scripts/make-deps-tarball.sh v0.4.0
# then host it and update the second SRC_URI line to point at it. See
# contrib/gentoo/README.md.
SRC_URI="
	https://git.sr.ht/~uid/tie/archive/v${PV}.tar.gz -> ${P}.tar.gz
	https://git.sr.ht/~uid/tie/refs/download/v${PV}/${P}-deps.tar.xz
"
S="${WORKDIR}/${PN}-v${PV}"

LICENSE="BSD"
SLOT="0"
KEYWORDS="~amd64 ~arm64"

# fuse is optional: it is only needed for the `tie mount` FUSE filesystem. The
# CLI, daemon, and filehost all work without it.
IUSE="+fuse"

RDEPEND="
	acct-group/tie
	acct-user/tie
	fuse? ( sys-fs/fuse:3 )
"
DEPEND="${RDEPEND}"

DOCS=( README.md CHANGELOG.md docs/ )

src_compile() {
	local ldflags="-X git.sr.ht/~uid/tie/version.Version=v${PV}"
	local cmd
	for cmd in tie tie-daemon tie-filehost; do
		ego build -ldflags "${ldflags}" -o "${cmd}" "./cmd/${cmd}"
	done
}

src_install() {
	dobin tie tie-daemon tie-filehost

	# Config templates. The daemon/filehost read /etc/tie/*.toml; ship the
	# examples as the default config (CONFIG_PROTECT keeps upgrades from
	# clobbering a customized file).
	insinto /etc/tie
	newins cmd/tie-daemon/tie-daemon.toml.example tie-daemon.toml
	newins cmd/tie-filehost/tie-filehost.toml.example tie-filehost.toml

	# Service files. The shared copies target /usr/local/bin (used by
	# install.sh); dobin installs to /usr/bin, so retarget them here.
	sed -i 's|/usr/local/bin/|/usr/bin/|g' \
		contrib/openrc/tie-daemon contrib/openrc/tie-filehost \
		contrib/systemd/tie-daemon.service contrib/systemd/tie-filehost.service || die

	newinitd contrib/openrc/tie-daemon tie-daemon
	newinitd contrib/openrc/tie-filehost tie-filehost
	systemd_dounit contrib/systemd/tie-daemon.service
	systemd_dounit contrib/systemd/tie-filehost.service

	# Data dirs owned by the service user.
	keepdir /var/lib/tie/db /var/lib/tie/data
	fowners -R tie:tie /var/lib/tie
	fperms 0750 /var/lib/tie

	einstalldocs
}

pkg_postinst() {
	elog "tie-daemon (triple store) listens on :1161 and tie-filehost"
	elog "(blob store) on :1162, both plain HTTP on all interfaces by default."
	elog
	elog "Before starting, edit the config, especially the [[Users]] account:"
	elog "    ${EROOT}/etc/tie/tie-daemon.toml"
	elog "    ${EROOT}/etc/tie/tie-filehost.toml"
	elog
	elog "Then enable the services, e.g. with OpenRC:"
	elog "    rc-service tie-daemon start"
	elog "    rc-service tie-filehost start"
	elog "or with systemd:"
	elog "    systemctl enable --now tie-daemon tie-filehost"
	elog
	elog "The default HTTP ports bind 0.0.0.0, so every host on your LAN can"
	elog "reach them unencrypted. Bind ListenOn to 127.0.0.1, or set"
	elog "CertFile/KeyFile (or a reverse proxy) for TLS, before exposing them"
	elog "beyond a trusted network. See ${EROOT}/usr/share/doc/${PF}."
}
