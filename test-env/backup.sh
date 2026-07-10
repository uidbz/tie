#!/usr/bin/env bash
# Dump the current collection to a TSV file, or restore one.
# Usage:
#   ./backup.sh dump    [file]   # default: backups/<collection>-<n>.tsv (stdout if -)
#   ./backup.sh restore <file>   # additive restore into the current collection
set -euo pipefail
source "$(dirname "$0")/env.sh"

cmd="${1:-}"; shift || true
mkdir -p "$TIE_ENV/backups"

case "$cmd" in
	dump)
		out="${1:-$TIE_ENV/backups/dump.tsv}"
		if [[ "$out" == "-" ]]; then
			tie_cli dump
		else
			tie_cli dump > "$out"
			echo "Wrote $(wc -l < "$out") triples to $out"
		fi
		;;
	restore)
		in="${1:-}"
		if [[ -z "$in" || ! -f "$in" ]]; then
			echo "Usage: $0 restore <file>" >&2
			exit 1
		fi
		tie_cli restore < "$in"
		;;
	*)
		echo "Usage: $0 {dump [file|-]|restore <file>}" >&2
		exit 1
		;;
esac
