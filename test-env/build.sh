#!/usr/bin/env bash
# Build the tie binaries into ./bin.
set -euo pipefail
source "$(dirname "$0")/env.sh"

echo "Building tie binaries from $TIE_SRC into $TIE_BIN ..."
cd "$TIE_SRC"

go build -o "$TIE_BIN/tie"          ./cmd/tie
go build -o "$TIE_BIN/tie-triplestore" ./cmd/tie-triplestore
go build -o "$TIE_BIN/tie-filehost" ./cmd/tie-filehost

echo "Built:"
ls -1 "$TIE_BIN"
