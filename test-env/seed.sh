#!/usr/bin/env bash
# Populate the stores with sample data:
#   1. create a few sample files + a sample directory
#   2. upload their bytes to the filehost (content-addressed)
#   3. write the tag triples the DB mount reads, so they appear under /by-tag
#
# The tag schema (matches client.Tag):
#   (hash,   "filename", <name>)
#   (hash,   "filesize", <bytes>)
#   (hash,   "tag",      <tag>)      forward: file -> tag
#   ("tags", "all",      <tag>)      registry of known tags
#   (hash,   "tie-type", "directory")  marks a tagged dir (expands as a tiedir)
set -euo pipefail
source "$(dirname "$0")/env.sh"

if ! curl -s -o /dev/null "$TIE_FILEHOST_URL/"; then
	echo "Filehost not reachable. Run ./start.sh first." >&2
	exit 1
fi

echo "== Creating sample files under $TIE_SAMPLES =="
rm -rf "$TIE_SAMPLES"
mkdir -p "$TIE_SAMPLES/holiday-pics"
echo "a note about the beach"        > "$TIE_SAMPLES/beach.txt"
echo "mountains are tall"            > "$TIE_SAMPLES/mountain.txt"
echo "receipt: 42.00"                > "$TIE_SAMPLES/receipt.txt"
echo "first photo caption"           > "$TIE_SAMPLES/holiday-pics/pic1.txt"
echo "second photo caption"          > "$TIE_SAMPLES/holiday-pics/pic2.txt"

# upload_file <path> -> prints hash
upload_file() {
	"$TIE_BIN/tie-upload" "$1" -server "$TIE_FILEHOST_HOST" -insecure | awk 'NR==1{print $1}'
}

# tag_common <hash> <filename> <size>
tag_common() {
	tie_cli add "$1" filename "$2" >/dev/null
	tie_cli add "$1" filesize "$3" >/dev/null
}

# add_tag <hash> <tag>
add_tag() {
	tie_cli add "$1" tag "$2" >/dev/null
	tie_cli add tags all "$2" >/dev/null
}

echo "== Uploading + tagging files =="

BEACH_HASH=$(upload_file "$TIE_SAMPLES/beach.txt")
tag_common "$BEACH_HASH" beach.txt "$(stat -c%s "$TIE_SAMPLES/beach.txt")"
add_tag "$BEACH_HASH" vacation
add_tag "$BEACH_HASH" outdoors
echo "  beach.txt      -> $BEACH_HASH  [vacation, outdoors]"

MOUNTAIN_HASH=$(upload_file "$TIE_SAMPLES/mountain.txt")
tag_common "$MOUNTAIN_HASH" mountain.txt "$(stat -c%s "$TIE_SAMPLES/mountain.txt")"
add_tag "$MOUNTAIN_HASH" outdoors
echo "  mountain.txt   -> $MOUNTAIN_HASH  [outdoors]"

RECEIPT_HASH=$(upload_file "$TIE_SAMPLES/receipt.txt")
tag_common "$RECEIPT_HASH" receipt.txt "$(stat -c%s "$TIE_SAMPLES/receipt.txt")"
add_tag "$RECEIPT_HASH" finance
echo "  receipt.txt    -> $RECEIPT_HASH  [finance]"

echo "== Uploading + tagging a directory (as an immutable tiedir blob) =="
# Uploading a directory returns one hash per entry plus the dir blob hash last.
DIR_HASH=$("$TIE_BIN/tie-upload" "$TIE_SAMPLES/holiday-pics" -server "$TIE_FILEHOST_HOST" -insecure | awk 'END{print $1}')
tag_common "$DIR_HASH" holiday-pics 0
tie_cli add "$DIR_HASH" tie-type directory >/dev/null
add_tag "$DIR_HASH" vacation
echo "  holiday-pics/  -> $DIR_HASH  [vacation]  (dir)"

echo "== Syncing =="
# nudge the daemon to flush; a Get triggers a sync path, but Sync is explicit:
tie_cli get -f all tags >/dev/null 2>&1 || true

echo
echo "Seed complete. Known tags:"
tie_cli get -f all tags 2>/dev/null | awk '{print "  - "$3}'
echo
echo "Now mount with:  ./mount-db.sh   (then browse $TIE_MNT/by-tag)"
