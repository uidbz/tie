"""Magic-byte media-type detection and tie-type classification.

A pragmatic pure-Python subset of github.com/h2non/filetype (used by the Go
client). It covers the common image/video/audio/document/archive signatures.
Detection feeds only the descriptive ``media-type`` and ``tie-type`` metadata
triples — the content hash never depends on it — so an unrecognized type
degrades gracefully to ``application/octet-stream`` / ``unknown-file`` rather
than breaking interop.
"""

from __future__ import annotations

from .vocab import TieType

IMAGE = "image"
VIDEO = "video"
AUDIO = "audio"
DOCUMENT = "document"
ARCHIVE = "archive"


def _ftyp_brand(buf: bytes) -> bytes:
    # ISO base media (mp4/mov/m4a/...): bytes 4..8 == 'ftyp', brand at 8..12.
    if len(buf) >= 12 and buf[4:8] == b"ftyp":
        return buf[8:12]
    return b""


def detect(buf: bytes) -> tuple[str, str | None]:
    """Return (mime, category) for the leading bytes, category None if unknown."""
    b = buf
    n = len(b)

    # --- images ---
    if b[:3] == b"\xff\xd8\xff":
        return "image/jpeg", IMAGE
    if b[:8] == b"\x89PNG\r\n\x1a\n":
        return "image/png", IMAGE
    if b[:6] in (b"GIF87a", b"GIF89a"):
        return "image/gif", IMAGE
    if n >= 12 and b[:4] == b"RIFF" and b[8:12] == b"WEBP":
        return "image/webp", IMAGE
    if b[:2] == b"BM":
        return "image/bmp", IMAGE
    if b[:4] in (b"II*\x00", b"MM\x00*"):
        return "image/tiff", IMAGE
    if b[:4] == b"\x00\x00\x01\x00":
        return "image/x-icon", IMAGE
    brand = _ftyp_brand(b)
    if brand[:4] in (b"heic", b"heix", b"hevc", b"mif1", b"heim", b"heis"):
        return "image/heif", IMAGE

    # --- audio (check before generic video ftyp) ---
    if b[:3] == b"ID3":
        return "audio/mpeg", AUDIO
    if b[:2] in (b"\xff\xfb", b"\xff\xf3", b"\xff\xf2"):
        return "audio/mpeg", AUDIO
    if b[:4] == b"fLaC":
        return "audio/x-flac", AUDIO
    if b[:4] == b"OggS":
        return "audio/ogg", AUDIO
    if n >= 12 and b[:4] == b"RIFF" and b[8:12] == b"WAVE":
        return "audio/x-wav", AUDIO
    if b[:4] == b"MThd":
        return "audio/midi", AUDIO
    if brand[:3] == b"M4A":
        return "audio/mp4", AUDIO

    # --- video ---
    if brand:
        # Common video brands: isom, iso2, mp41, mp42, avc1, qt, 3gp*, M4V*
        if brand[:2] == b"qt":
            return "video/quicktime", VIDEO
        if brand[:3] in (b"3gp", b"3g2"):
            return "video/3gpp", VIDEO
        if brand[:3] == b"M4V":
            return "video/x-m4v", VIDEO
        return "video/mp4", VIDEO
    if b[:4] == b"\x1aE\xdf\xa3":
        # EBML: matroska or webm. filetype reports matroska by default.
        return "video/x-matroska", VIDEO
    if n >= 12 and b[:4] == b"RIFF" and b[8:12] == b"AVI ":
        return "video/x-msvideo", VIDEO
    if b[:3] == b"FLV":
        return "video/x-flv", VIDEO
    if b[:3] == b"\x00\x00\x01" and n >= 4 and 0xB0 <= b[3] <= 0xBF:
        return "video/mpeg", VIDEO

    # --- documents ---
    if b[:4] == b"%PDF":
        return "application/pdf", DOCUMENT
    if b[:8] == b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1":
        # OLE compound (legacy doc/xls/ppt); report as msword-ish document.
        return "application/msword", DOCUMENT

    # --- archives ---
    if b[:4] == b"PK\x03\x04" or b[:4] == b"PK\x05\x06" or b[:4] == b"PK\x07\x08":
        return "application/zip", ARCHIVE
    if b[:2] == b"\x1f\x8b":
        return "application/gzip", ARCHIVE
    if b[:3] == b"BZh":
        return "application/x-bzip2", ARCHIVE
    if b[:6] == b"7z\xbc\xaf\x27\x1c":
        return "application/x-7z-compressed", ARCHIVE
    if b[:6] == b"\xfd7zXZ\x00":
        return "application/x-xz", ARCHIVE
    if b[:7] == b"Rar!\x1a\x07\x00" or b[:8] == b"Rar!\x1a\x07\x01\x00":
        return "application/x-rar-compressed", ARCHIVE
    if n >= 262 and b[257:262] == b"ustar":
        return "application/x-tar", ARCHIVE

    return "application/octet-stream", None


def media_type(buf: bytes) -> str:
    return detect(buf)[0]


def tie_type_of(buf: bytes) -> str:
    """Classify leading bytes into a file tie-type (mirrors client.GetTieType)."""
    _, cat = detect(buf)
    return {
        IMAGE: TieType.IMAGE_FILE,
        VIDEO: TieType.VIDEO_FILE,
        AUDIO: TieType.AUDIO_FILE,
        DOCUMENT: TieType.DOCUMENT_FILE,
        ARCHIVE: TieType.ARCHIVE_FILE,
    }.get(cat, TieType.UNKNOWN_FILE)
