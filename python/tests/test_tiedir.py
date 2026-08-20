"""Byte-exactness of the tiedir-v2 manifest format."""

import base64

from tie_client import tiedir
from tie_client.tiedir import DirEntry


def test_manifest_bytes_exact():
    head = b"hello world"
    entries = [
        DirEntry(hash="a" * 64, filename="song.flac", size=123, head=head),
        DirEntry(hash="b" * 64, filename="cover.jpg", size=45, head=b""),
    ]
    manifest = tiedir.build_manifest(entries)
    expected = (
        b"tiedir-v2\n---\n"
        + b"a" * 64 + b"\tsong.flac\t123\t" + base64.b64encode(head) + b"\n"
        + b"b" * 64 + b"\tcover.jpg\t45\t\n"
    )
    assert manifest == expected


def test_manifest_round_trip():
    entries = [DirEntry(hash="c" * 64, filename="f.txt", size=7, head=b"abc\tdef\n")]
    manifest = tiedir.build_manifest(entries)
    parsed = tiedir.parse_manifest(manifest)
    assert len(parsed) == 1
    assert parsed[0].hash == "c" * 64
    assert parsed[0].filename == "f.txt"
    assert parsed[0].size == 7
    assert parsed[0].head == b"abc\tdef\n"  # tabs/newlines survive base64


def test_is_hex_hash():
    assert tiedir.is_hex_hash("0" * 64)
    assert not tiedir.is_hex_hash("0" * 63)
    assert not tiedir.is_hex_hash("g" * 64)
    assert not tiedir.is_hex_hash("A" * 64)  # uppercase rejected


def test_parse_rejects_traversal_name():
    line = "a" * 64 + "\t../evil\t1\t"
    assert tiedir.parse_dir_line(line) is None


def test_dir_head_detection():
    assert tiedir.looks_like_dir(b"tiedir-v2\n---\nrest")
    assert not tiedir.looks_like_dir(b"\x89PNG")
    assert tiedir.is_dir_head(b"tiedir-v2\n---\n")
