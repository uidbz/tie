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
# The EXIT trap must never change the script's own status: since Go 1.21 the
# required toolchain is downloaded into GOMODCACHE as a read-only module
# (golang.org/toolchain@...), which makes a plain rm -rf fail even after the
# tarball was written successfully. -modcacherw does not cover it.
cleanup() {
	chmod -R u+w "$WORK" 2>/dev/null || true
	rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

echo "==> cloning tie at ${TAG}"
git clone --quiet --depth 1 --branch "$TAG" https://github.com/uidbz/tie "$WORK/src"

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
#
# The golang.org/toolchain@... entry is left out on purpose: since Go 1.21 a
# GOTOOLCHAIN=auto run downloads the required Go distribution into GOMODCACHE as
# a module, and packing it would put a whole toolchain into an artifact whose job
# is to carry tie's dependencies. Portage builds against the system Go, so the
# version requirement belongs in the ebuild instead — BDEPEND=">=dev-lang/go-…"
# in net-misc/tie, matching go.mod's go directive. Both the unpacked module
# directory and the download-cache zip (plus its sumdb lookup line) are excluded.
EXCLUDES=(
	--exclude='go-mod/golang.org/toolchain*'
	--exclude='go-mod/cache/download/golang.org/toolchain*'
	--exclude='go-mod/cache/download/sumdb/sum.golang.org/lookup/golang.org/toolchain*'
)
XZ_OPT='-T0 -9' tar -acf "$OUT" "${EXCLUDES[@]}" -C "$WORK" go-mod

echo
echo "Wrote: $OUT"
echo "Next:"
echo "  1. Host it at the ebuild's second SRC_URI (or copy to /var/cache/distfiles/ for a local test)."
echo "  2. cd into the ebuild dir and run: ebuild tie-${VER}.ebuild manifest"
