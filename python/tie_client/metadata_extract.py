"""Best-effort embedded audio metadata extraction.

Mirrors client.ExtractMediaMetadata: audio files yield title/artist/album/
year/track; anything else (or any parse failure) yields empty metadata, so the
caller falls back to path-based placement. This is a pragmatic pure-Python
reader covering the common cases (ID3v2 for MP3, Vorbis comments for FLAC);
formats it does not understand simply return empty, which is non-fatal.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass


@dataclass
class Media:
    title: str = ""
    artist: str = ""
    album: str = ""
    year: int = 0
    track: int = 0


def _first_int(s: str) -> int:
    num = ""
    for ch in s.strip():
        if ch.isdigit():
            num += ch
        else:
            break
    try:
        return int(num) if num else 0
    except ValueError:
        return 0


def _extract_id3v2(data: bytes) -> Media:
    if data[:3] != b"ID3" or len(data) < 10:
        return Media()
    version = data[3]
    # Synchsafe 28-bit size of the tag body.
    size = (data[6] << 21) | (data[7] << 14) | (data[8] << 7) | data[9]
    body = data[10 : 10 + size]
    m = Media()
    i = 0
    # ID3v2.2 uses 3-byte frame ids + 3-byte sizes; v2.3/2.4 use 4+4.
    if version == 2:
        id_len, size_len = 3, 3
    else:
        id_len, size_len = 4, 4
    while i + id_len + size_len <= len(body):
        frame_id = body[i : i + id_len]
        if frame_id == b"\x00" * id_len:
            break
        raw = body[i + id_len : i + id_len + size_len]
        if size_len == 4:
            if version == 4:  # synchsafe
                fsize = (raw[0] << 21) | (raw[1] << 14) | (raw[2] << 7) | raw[3]
            else:
                fsize = struct.unpack(">I", raw)[0]
            header = id_len + size_len + 2  # + 2 flag bytes in v2.3/2.4
        else:
            fsize = (raw[0] << 16) | (raw[1] << 8) | raw[2]
            header = id_len + size_len
        start = i + header
        payload = body[start : start + fsize]
        i = start + fsize
        if not payload:
            continue
        text = _decode_text_frame(payload)
        fid = frame_id.decode("latin-1", "replace")
        if fid in ("TIT2", "TT2"):
            m.title = text
        elif fid in ("TPE1", "TP1"):
            m.artist = text
        elif fid in ("TALB", "TAL"):
            m.album = text
        elif fid in ("TYER", "TYE", "TDRC"):
            m.year = _first_int(text)
        elif fid in ("TRCK", "TRK"):
            m.track = _first_int(text)
    return m


def _decode_text_frame(payload: bytes) -> str:
    encoding = payload[0]
    raw = payload[1:]
    try:
        if encoding == 0:
            s = raw.decode("latin-1")
        elif encoding == 1:
            s = raw.decode("utf-16")
        elif encoding == 2:
            s = raw.decode("utf-16-be")
        else:
            s = raw.decode("utf-8")
    except Exception:
        s = raw.decode("latin-1", "replace")
    return s.split("\x00", 1)[0].strip()


def _extract_flac(f) -> Media:
    if f.read(4) != b"fLaC":
        return Media()
    m = Media()
    while True:
        header = f.read(4)
        if len(header) < 4:
            break
        block_type = header[0] & 0x7F
        last = bool(header[0] & 0x80)
        length = (header[1] << 16) | (header[2] << 8) | header[3]
        block = f.read(length)
        if block_type == 4:  # VORBIS_COMMENT
            _parse_vorbis(block, m)
        if last:
            break
    return m


def _parse_vorbis(block: bytes, m: Media) -> None:
    try:
        pos = 0
        vlen = struct.unpack("<I", block[pos : pos + 4])[0]
        pos += 4 + vlen  # skip vendor string
        count = struct.unpack("<I", block[pos : pos + 4])[0]
        pos += 4
        for _ in range(count):
            clen = struct.unpack("<I", block[pos : pos + 4])[0]
            pos += 4
            comment = block[pos : pos + clen].decode("utf-8", "replace")
            pos += clen
            if "=" not in comment:
                continue
            key, _, value = comment.partition("=")
            key = key.upper()
            if key == "TITLE":
                m.title = value
            elif key == "ARTIST":
                m.artist = value
            elif key == "ALBUM":
                m.album = value
            elif key == "DATE":
                m.year = _first_int(value)
            elif key == "TRACKNUMBER":
                m.track = _first_int(value)
    except (struct.error, IndexError):
        pass


def extract_media_metadata(path: str) -> Media:
    try:
        with open(path, "rb") as f:
            head = f.read(4)
            f.seek(0)
            if head == b"fLaC":
                return _extract_flac(f)
            if head[:3] == b"ID3":
                return _extract_id3v2(f.read())
    except OSError:
        return Media()
    return Media()
