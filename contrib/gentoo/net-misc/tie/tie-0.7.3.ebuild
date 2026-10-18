# Copyright 2023-2026 Johan Straarup
# Distributed under the terms of the BSD 3-Clause License

EAPI=8

inherit go-module systemd

DESCRIPTION="Triple-store client with a content-addressed filehost and FUSE mount"
HOMEPAGE="https://github.com/uidbz/tie"

# Two source artifacts:
#   1. the release source archive from GitHub, and
#   2. a dependency tarball with every Go module the build needs, so the build
#      runs fully offline inside Portage's network sandbox (the ::gentoo way).
#
# Regenerate the deps tarball for a new release with:
#     contrib/gentoo/scripts/make-deps-tarball.sh v0.7.3
# then attach it to the GitHub release so the second SRC_URI line resolves. See
# contrib/gentoo/README.md.
SRC_URI="
	https://github.com/uidbz/tie/archive/refs/tags/v${PV}.tar.gz -> ${P}.tar.gz
	https://github.com/uidbz/tie/releases/download/v${PV}/${P}-deps.tar.xz
"
S="${WORKDIR}/${PN}-${PV}"

LICENSE="BSD"
SLOT="0"
KEYWORDS="~amd64 ~arm64"

# fuse is optional: it is only needed for the `tie mount` FUSE filesystem. The
# CLI, triple store, filehost and SMB server all work without it.
IUSE="+fuse"

RDEPEND="
	acct-group/tie
	acct-user/tie
	fuse? ( sys-fs/fuse:3 )
"
DEPEND="${RDEPEND}"

# go.mod's go directive is a build requirement, not a suggestion: with the
# network sandbox on, an older system Go cannot fetch a newer toolchain and the
# build fails inside Portage. Keep this in step with go.mod.
BDEPEND=">=dev-lang/go-1.27.2"

DOCS=( README.md CHANGELOG.md docs/ )

src_compile() {
	local ldflags="-X github.com/uidbz/tie/version.Version=v${PV}"
	local cmd
	for cmd in tie tie-triplestore tie-filehost tie-smb; do
		ego build -ldflags "${ldflags}" -o "${cmd}" "./cmd/${cmd}"
	done
}

src_install() {
	dobin tie tie-triplestore tie-filehost tie-smb

	# Config templates. The servers read /etc/tie/*.toml; ship the examples as
	# the default config (CONFIG_PROTECT keeps upgrades from clobbering a
	# customized file).
	insinto /etc/tie
	newins cmd/tie-triplestore/tie-triplestore.toml.example tie-triplestore.toml
	newins cmd/tie-filehost/tie-filehost.toml.example tie-filehost.toml
	newins cmd/tie-smb/tie-smb.toml.example tie-smb.toml

	# Service files. The shared copies target /usr/local/bin (used by
	# install.sh); dobin installs to /usr/bin, so retarget them here.
	sed -i 's|/usr/local/bin/|/usr/bin/|g' \
		contrib/openrc/tie-triplestore contrib/openrc/tie-filehost contrib/openrc/tie-smb \
		contrib/systemd/tie-triplestore.service contrib/systemd/tie-filehost.service \
		contrib/systemd/tie-smb.service || die

	newinitd contrib/openrc/tie-triplestore tie-triplestore
	newinitd contrib/openrc/tie-filehost tie-filehost
	newinitd contrib/openrc/tie-smb tie-smb
	systemd_dounit contrib/systemd/tie-triplestore.service
	systemd_dounit contrib/systemd/tie-filehost.service
	systemd_dounit contrib/systemd/tie-smb.service

	# Data dirs owned by the service user.
	keepdir /var/lib/tie/db /var/lib/tie/data
	fowners -R tie:tie /var/lib/tie
	fperms 0750 /var/lib/tie

	einstalldocs
}

pkg_postinst() {
	elog "tie-triplestore (triple store) listens on :1161 and tie-filehost"
	elog "(blob store) on :1162, both plain HTTP on all interfaces by default."
	elog
	elog "Before starting, edit the config, especially the [[Users]] account:"
	elog "    ${EROOT}/etc/tie/tie-triplestore.toml"
	elog "    ${EROOT}/etc/tie/tie-filehost.toml"
	elog
	elog "Then enable the services, e.g. with OpenRC:"
	elog "    rc-service tie-triplestore start"
	elog "    rc-service tie-filehost start"
	elog "or with systemd:"
	elog "    systemctl enable --now tie-triplestore tie-filehost"
	elog
	elog "tie-smb (the SMB file server on port 445) is installed but enabled by"
	elog "nothing. It reads the tie client config named by its TieConfig key"
	elog "(default ${EROOT}/etc/tie/config.toml) and needs its own [[Users]]."
	elog "Port 445 is privileged: the systemd unit grants CAP_NET_BIND_SERVICE;"
	elog "under OpenRC set it on the binary with"
	elog "    setcap 'cap_net_bind_service=+ep' /usr/bin/tie-smb"
	elog "or point ListenOn at a high port and mount with that port explicitly."
	elog
	elog "The default HTTP ports bind 0.0.0.0, so every host on your LAN can"
	elog "reach them unencrypted. Bind ListenOn to 127.0.0.1, or set"
	elog "CertFile/KeyFile (or a reverse proxy) for TLS, before exposing them"
	elog "beyond a trusted network. See ${EROOT}/usr/share/doc/${PF}."
}
