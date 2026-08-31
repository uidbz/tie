# Installing tie on Gentoo

These ebuilds install `tie`, `tie-triplestore`, and `tie-filehost` as a normal
Gentoo package: binaries in `/usr/bin`, config in `/etc/tie/`, data in
`/var/lib/tie/`, plus OpenRC and systemd service files and a dedicated `tie`
service user/group.

Two ebuilds are provided:

| Ebuild             | Builds from                  | Go deps                                  |
|--------------------|------------------------------|------------------------------------------|
| `tie-0.4.0.ebuild` | the `v0.4.0` release tarball | offline, from a pre-built deps tarball   |
| `tie-9999.ebuild`  | git `master` (live)          | fetched from the network at build time   |

The release ebuild builds fully offline inside Portage's network sandbox — the
`::gentoo` requirement — by consuming a **dependency tarball** produced ahead of
time (see [Maintaining the release ebuild](#maintaining-the-release-ebuild)).
The live `-9999` ebuild fetches modules over the network at build time
(`RESTRICT="network-sandbox"`); that is normal for live ebuilds, which never
enter the official tree.

## 1. Add a local overlay

Portage only installs ebuilds from a configured repository, so drop these into
a local overlay. If you do not already have one:

```sh
sudo mkdir -p /var/db/repos/localtie/{metadata,profiles}
echo 'localtie'                     | sudo tee /var/db/repos/localtie/profiles/repo_name
echo 'masters = gentoo'             | sudo tee    /var/db/repos/localtie/metadata/layout.conf
echo 'thin-manifests = true'        | sudo tee -a /var/db/repos/localtie/metadata/layout.conf

sudo tee /etc/portage/repos.conf/localtie.conf >/dev/null <<'EOF'
[localtie]
location = /var/db/repos/localtie
masters = gentoo
auto-sync = no
EOF
```

Copy this directory's contents into the overlay, preserving the
category/package layout:

```sh
# from a checkout of the tie repo:
sudo cp -r contrib/gentoo/net-misc   /var/db/repos/localtie/
sudo cp -r contrib/gentoo/acct-user  /var/db/repos/localtie/
sudo cp -r contrib/gentoo/acct-group /var/db/repos/localtie/
```

## 2a. Release build (`tie-0.4.0`)

The release ebuild needs its dependency tarball available before it can build.
For a release you published, that artifact is hosted at the `SRC_URI` and
Portage fetches it automatically. To build locally before publishing, generate
the tarball and drop it in your distfiles dir:

```sh
# from a checkout of the tie repo:
contrib/gentoo/scripts/make-deps-tarball.sh v0.4.0
sudo cp tie-0.4.0-deps.tar.xz /var/cache/distfiles/
```

Then generate the Manifest and emerge:

```sh
cd /var/db/repos/localtie/net-misc/tie
sudo ebuild tie-0.4.0.ebuild manifest
sudo emerge -av net-misc/tie
```

## 2b. Live build (`tie-9999`)

The live ebuild builds from `master` and lets Go fetch modules over the network
at build time, so it needs network access. It already sets
`RESTRICT="network-sandbox"` to allow that. Unmask the live ebuild and emerge:

```sh
echo '=net-misc/tie-9999 **' | sudo tee /etc/portage/package.accept_keywords/tie
sudo emerge -av =net-misc/tie-9999
```

Re-emerge any time to track the latest `master`.

## 3. USE flags

| Flag   | Default | Effect                                                    |
|--------|---------|-----------------------------------------------------------|
| `fuse` | on      | pulls in `sys-fs/fuse:3`, required for `tie mount`        |

Disable it (`USE="-fuse"`) if you only need the CLI/triplestore/filehost and not
the FUSE mount.

## 4. Configure and start

The package installs default config to `/etc/tie/` (protected by
`CONFIG_PROTECT`, so upgrades never overwrite your edits). Before starting,
edit at least the triplestore's `[[Users]]` account:

```sh
sudo nano /etc/tie/tie-triplestore.toml     # set [[Users]] Username/Password
sudo nano /etc/tie/tie-filehost.toml
```

Start the services.

OpenRC:

```sh
sudo rc-update add tie-triplestore default
sudo rc-update add tie-filehost default
sudo rc-service tie-triplestore start
sudo rc-service tie-filehost start
```

systemd:

```sh
sudo systemctl enable --now tie-triplestore tie-filehost
```

Then point the CLI at them. Running `tie` with no config writes a default one
(plain `http://localhost:1161` / `:1162`) to `~/.config/tie/config.toml`; edit
it to match the credentials you set above.

## 5. Network exposure

The defaults are plain HTTP (`Insecure = true`) with `ListenOn = ":116x"`,
which binds **all interfaces** (`0.0.0.0`) — every host on your LAN can reach
the services unencrypted, with only HTTP basic auth. That suits a personal
library on a trusted home network. For local-only access set
`ListenOn = "127.0.0.1:116x"`; for TLS set `CertFile`/`KeyFile` (or run a
reverse proxy and keep `Insecure = true` bound to localhost). Do not expose
these ports to an untrusted network as-is. See the top-level `README.md` and
`contrib/README.md` for details.

## Maintaining the release ebuild

Each release needs a matching dependency tarball, because Portage builds Go
packages offline inside a network sandbox. Whenever you tag a release:

```sh
# 1. Build the deps tarball for the tag.
contrib/gentoo/scripts/make-deps-tarball.sh v0.4.0

# 2. Attach tie-0.4.0-deps.tar.xz to the GitHub release as an asset, so the
#    ebuild's second SRC_URI can fetch it. Upload with the gh CLI:
gh release upload v0.4.0 tie-0.4.0-deps.tar.xz
#    After upload the ebuild's URL resolves:
#      https://github.com/uidbz/tie/releases/download/v0.4.0/tie-0.4.0-deps.tar.xz

# 3. Copy the ebuild for the new version and regenerate the Manifest.
cd contrib/gentoo/net-misc/tie
cp tie-0.4.0.ebuild tie-<newver>.ebuild   # SRC_URI derives the tag from ${PV}
ebuild tie-<newver>.ebuild manifest
```

The dependency tarball is the approach the current `go-module.eclass` mandates:
`EGO_SUM` (an inline module list) is deprecated in the eclass, explicitly
"replaced by a dependency tarball". So the tarball step above is required for
every release — there is no inline-list alternative to fall back on.

## Upstreaming to ::gentoo

These files are laid out as three ready-to-submit packages
(`net-misc/tie`, `acct-user/tie`, `acct-group/tie`), each with a
`metadata.xml`. The intended route is **proxy maintainership**: you stay the
upstream/proxied maintainer and the `proxy-maint@gentoo.org` project sponsors
the packages (already listed in each `metadata.xml`).

Before submitting, on an actual Gentoo system:

- `pkgcheck scan` (from `dev-util/pkgcheck`) must be clean for all three
  packages — this catches metadata, dependency, and Manifest issues that cannot
  be verified off a Gentoo box.
- `ebuild ... manifest` must succeed with the deps tarball reachable.
- `LICENSE` must cover the statically linked deps, not just tie's own BSD.
  `go-module.eclass` recommends `dev-go/lichen` to extract the full set from the
  built binary; audit and expand `LICENSE` accordingly before submitting.
- Once accepted, the deps tarball is mirrored on Gentoo's distfiles, and the
  `SRC_URI` typically points at a dev-space URL your proxy-maint sponsor sets
  up; the GitHub release asset URL is the pre-acceptance self-hosting option.
- Drop `KEYWORDS` to a single arch you can actually test (e.g. `~amd64`) unless
  you have arm64 hardware to verify on.

The `-9999` live ebuild is for overlays only; do not submit it to `::gentoo`.

### Why EAPI 8 (not 9)

The ebuilds are pinned to `EAPI=8` even though `go-module`, `acct-user`,
`acct-group`, and `git-r3` all support EAPI 9. The blocker is `systemd.eclass`,
which supports only EAPI 7 and 8; setting `EAPI=9` makes it `die` at inherit.
We keep the systemd eclass on purpose — it installs the systemd units to the
correct path so tie works under either init system (Gentoo is about choice),
and inheriting it adds no dependency on systemd itself. Hand-rolling the unit
install to escape the eclass would hardcode the unit path the eclass exists to
abstract, which is worse practice. Revisit the EAPI bump only once
`systemd.eclass` gains EAPI 9 support; it is then a one-line change per ebuild.

> Note: the ebuilds and `metadata.xml` here were authored to Gentoo conventions
> but have not been run through `pkgcheck`/`emerge` on a Gentoo host. Treat a
> clean `pkgcheck scan` as the gate before upstreaming.
