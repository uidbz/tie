#!/usr/bin/env bash
# Generate the Go dependency tarball that the release ebuild consumes.
#
# Gentoo builds Go packages offline inside a network sandbox, so every module
# the build needs must be present up front. This script downloads the complete
# module set for a given tag into a GOMODCACHE and packs it as
# tie-<version>-deps.tar.xz, matching the second SRC_URI entry in the release
# ebuild.
#
# Usage:
#     contrib/gentoo/scripts/make-deps-tarball.sh v0.4.0
#
# The resulting tarball must then be:
#   1. hosted somewhere the ebuild's SRC_URI can fetch it (or dropped into a
#      local /var/cache/distfiles for testing), and
#   2. checksummed into the package Manifest with `ebuild <ebuild> manifest`.
#
# Run from anywhere; it operates in a throwaway temp dir and writes the tarball
# to the current directory.
set -euo pipefail

TAG="${1:-}"
if [ -z "$TAG" ]; then
	echo "usage: $0 <git-tag>   e.g. $0 v0.4.0" >&2
	exit 1
fi

command -v go >/dev/null 2>&1 || { echo "error: go toolchain not found in PATH" >&2; exit 1; }

# version without the leading 'v', to match ${P} in the ebuild (tie-0.4.0).
VER="${TAG#v}"
OUT="$(pwd)/tie-${VER}-deps.tar.xz"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> cloning tie at ${TAG}"
git clone --quiet --depth 1 --branch "$TAG" https://git.sr.ht/~uid/tie "$WORK/src"

echo "==> downloading Go modules into a clean module cache"
# -modcacherw leaves the cache writable so the tarball can be unpacked and
# cleaned by Portage. GOFLAGS=-mod=mod ensures downloads even without vendoring.
GOMODCACHE="$WORK/go-mod" \
GOFLAGS=-mod=mod \
	go -C "$WORK/src" mod download -modcacherw all

echo "==> packing ${OUT}"
# Pack the cache directory itself (go-mod/) so it unpacks to ${WORKDIR}/go-mod,
# which go-module.eclass points GOMODCACHE at. XZ_OPT matches the eclass's
# recommendation: parallel compression (-T0) at max level (-9).
XZ_OPT='-T0 -9' tar -acf "$OUT" -C "$WORK" go-mod

echo
echo "Wrote: $OUT"
echo "Next:"
echo "  1. Host it at the ebuild's second SRC_URI (or copy to /var/cache/distfiles/ for a local test)."
echo "  2. cd into the ebuild dir and run: ebuild tie-${VER}.ebuild manifest"
