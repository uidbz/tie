#!/usr/bin/env bash
# Migrate persisted virtual-path triples from the old "file:" URI scheme to the
# new "tie:" scheme (a private, RFC 3986-conformant scheme; see FileURIScheme in
# client/tag.go). Only (uid, "path", "file:/…") triples are rewritten; every
# other triple is preserved verbatim.
#
# Mechanism: dump the collection, rewrite the path values, then `restore --drop`
# to overwrite the collection with the rewritten data. The daemon must be
# running (dump/restore go through it). Run once per collection you have — the
# collection is the one named in the given tie CLI config (Namespace/Collection).
#
# Usage:
#   ./migrate-scheme.sh [tie-config.toml]     # default: config.toml (this dir)
#   TIE=/path/to/tie ./migrate-scheme.sh /path/to/config.toml
#
# Idempotent: a second run finds no "file:" path triples and exits without
# touching the store. A timestamped backup of the pre-migration dump is always
# kept.
set -euo pipefail

TIE="${TIE:-tie}"
CONFIG="${1:-config.toml}"
TAB=$'\t'

ts=$(date +%Y%m%d-%H%M%S)
backup="scheme-migration-backup-${ts}.tsv"
rewritten="scheme-migration-${ts}.tsv"

echo "Dumping collection (config: $CONFIG) -> $backup" >&2
"$TIE" -c "$CONFIG" dump > "$backup"

count=$(grep -c "${TAB}path${TAB}file:" "$backup" || true)
echo "path triples on the old 'file:' scheme: $count" >&2
if [ "$count" -eq 0 ]; then
	echo "Nothing to migrate. Backup kept at $backup." >&2
	exit 0
fi

sed "s@${TAB}path${TAB}file:@${TAB}path${TAB}tie:@" "$backup" > "$rewritten"

echo "Restoring rewritten dump with --drop (overwrites the collection) ..." >&2
"$TIE" -c "$CONFIG" restore --drop "$rewritten"

echo "Done: migrated $count path triples to the 'tie:' scheme." >&2
echo "  backup (pre-migration): $backup" >&2
echo "  rewritten dump:         $rewritten" >&2
