# Copyright 2023-2026 Johan Straarup
# Distributed under the terms of the BSD 3-Clause License

EAPI=8

inherit go-module git-r3 systemd

DESCRIPTION="Triple-store client with a content-addressed filehost and FUSE mount"
HOMEPAGE="https://github.com/uidbz/tie"

# Live ebuild: builds from the master branch. There is no fixed release tarball
# to attach pre-vendored deps to, so Go fetches modules over the network during
# the build. This requires disabling the network sandbox for this package
# (RESTRICT below); see contrib/gentoo/README.md.
EGIT_REPO_URI="https://github.com/uidbz/tie"

LICENSE="BSD"
SLOT="0"
KEYWORDS=""
RESTRICT="network-sandbox"

IUSE="+fuse"

RDEPEND="
	acct-group/tie
	acct-user/tie
	fuse? ( sys-fs/fuse:3 )
"
DEPEND="${RDEPEND}"

DOCS=( README.md CHANGELOG.md docs/ )

src_compile() {
	local ver
	ver="$(git -C "${S}" describe --tags --always --dirty 2>/dev/null || echo 9999)"
	local ldflags="-X github.com/uidbz/tie/version.Version=${ver}"
	local cmd
	for cmd in tie tie-triplestore tie-filehost tie-smb; do
		ego build -ldflags "${ldflags}" -o "${cmd}" "./cmd/${cmd}"
	done
}

src_install() {
	dobin tie tie-triplestore tie-filehost tie-smb

	insinto /etc/tie
	newins cmd/tie-triplestore/tie-triplestore.toml.example tie-triplestore.toml
	newins cmd/tie-filehost/tie-filehost.toml.example tie-filehost.toml
	newins cmd/tie-smb/tie-smb.toml.example tie-smb.toml

	# The shared service files target /usr/local/bin (used by install.sh);
	# dobin installs to /usr/bin, so retarget them for the packaged layout.
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
	elog "    /etc/tie/tie-triplestore.toml"
	elog "    /etc/tie/tie-filehost.toml"
	elog
	elog "tie-smb (the SMB file server) is installed but enabled by nothing: it"
	elog "reads the tie client config named by its TieConfig key, and needs its"
	elog "own [[Users]]. Its default port 445 is privileged - the systemd unit"
	elog "grants CAP_NET_BIND_SERVICE; under OpenRC set it on the binary with"
	elog "    setcap 'cap_net_bind_service=+ep' /usr/bin/tie-smb"
	elog "or point ListenOn at a high port and mount with that port explicitly."
	elog
	elog "The default HTTP ports bind 0.0.0.0, so every host on your LAN can"
	elog "reach them unencrypted. Bind ListenOn to 127.0.0.1, or set"
	elog "CertFile/KeyFile (or a reverse proxy) for TLS, before exposing them"
	elog "beyond a trusted network."
}
