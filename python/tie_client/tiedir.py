"""The tiedir-v2 directory-manifest format.

A directory blob is a text manifest: the header ``tiedir-v2\\n---\\n`` followed
by one line per child, ``hash\\tfilename\\tsize\\tbase64(head)\\n``. The bytes
must match the Go implementation (metadata/tiedir.go) exactly so a directory
uploaded by this client hashes to the same content address as one uploaded by
the Go client — that is what makes identical trees dedupe.
"""

from __future__ import annotations

import base64
from dataclasses import dataclass

# Header prefixing every tiedir-v2 blob. Bytes, since it is hashed/compared raw.
DIR_HEADER = b"tiedir-v2\n---\n"

# Leading bytes needed to sniff a media type (metadata.MagicNumber).
MAGIC_NUMBER = 261


@dataclass
class DirEntry:
    hash: str
    filename: str
    size: int
    head: bytes

    def line(self) -> str:
        return (
            f"{self.hash}\t{self.filename}\t{self.size}\t"
            f"{base64.b64encode(self.head).decode('ascii')}\n"
        )


def is_hex_hash(s: str) -> bool:
    """True iff s is exactly 64 lowercase hex characters."""
    if len(s) != 64:
        return False
    return all(c in "0123456789abcdef" for c in s)


def is_dir_head(head: bytes) -> bool:
    return head[:8] == b"tiedir-v"


def _valid_entry_name(name: str) -> bool:
    if name in ("", ".", ".."):
        return False
    return "/" not in name and "\\" not in name


def build_manifest(entries: list[DirEntry]) -> bytes:
    """Assemble a manifest blob from child entries (in the given order)."""
    out = DIR_HEADER.decode("ascii")
    for e in entries:
        out += e.line()
    return out.encode("utf-8")


def parse_dir_line(line: str) -> DirEntry | None:
    parts = line.split("\t")
    if len(parts) != 4:
        return None
    if not is_hex_hash(parts[0]):
        return None
    if not _valid_entry_name(parts[1]):
        return None
    try:
        size = int(parts[2])
    except ValueError:
        return None
    try:
        head = base64.b64decode(parts[3], validate=True)
    except Exception:
        return None
    return DirEntry(hash=parts[0], filename=parts[1], size=size, head=head)


def parse_manifest(data: bytes) -> list[DirEntry]:
    """Parse a manifest blob into its child entries, skipping malformed lines."""
    text = data.decode("utf-8", errors="replace")
    if not text.startswith(DIR_HEADER.decode("ascii")):
        raise ValueError("not a tiedir-v2 manifest")
    body = text[len(DIR_HEADER):]
    entries = []
    for line in body.split("\n"):
        if not line:
            continue
        entry = parse_dir_line(line)
        if entry is not None:
            entries.append(entry)
    return entries


def looks_like_dir(data: bytes) -> bool:
    return data[: len(DIR_HEADER)] == DIR_HEADER
