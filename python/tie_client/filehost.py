"""Filehost (content-addressed blob store) client.

Uploads use ``PUT {host}/upload`` with the raw bytes as the body; the server
computes and returns the 64-hex content hash, so this client never needs to
reproduce the keyed HighwayHash. Directory trees are uploaded bottom-up: each
child is uploaded (yielding its hash), then a byte-identical tiedir-v2 manifest
is assembled and uploaded, yielding the directory hash. Downloads expand
directory manifests recursively. Byte verification is intentionally omitted
(the Go mount defaults to it off too).
"""

from __future__ import annotations

import json
import os
import ssl
import urllib.error
import urllib.request
from base64 import b64encode
from dataclasses import dataclass, field

from . import filetype, tiedir
from .config import FileHost
from .tiedir import DIR_HEADER, DirEntry

MAX_DEPTH = 128  # mirror getlib.maxDepth


@dataclass
class UploadedItem:
    hash: str
    filename: str
    media_type: str
    size: int
    head: bytes = b""
    error_msg: str = ""


@dataclass
class UploadResult:
    items: list[UploadedItem] = field(default_factory=list)
    error_msg: str = ""


class FilehostClient:
    def __init__(self, host: FileHost):
        self.host = host
        self.base = host.url.rstrip("/")
        self._ctx: ssl.SSLContext | None = None
        if self.base.startswith("https") and host.insecure:
            self._ctx = ssl.create_default_context()
            self._ctx.check_hostname = False
            self._ctx.verify_mode = ssl.CERT_NONE
        self._auth = None
        if host.username:
            self._auth = "Basic " + b64encode(
                f"{host.username}:{host.password}".encode()
            ).decode()

    def _prepare(self, req: urllib.request.Request) -> urllib.request.Request:
        if self._auth:
            req.add_header("Authorization", self._auth)
        return req

    # --- upload ---

    def _put(self, data, length: int, retention: str = "", owner: str = "") -> str:
        req = urllib.request.Request(self.base + "/upload", data=data, method="PUT")
        req.add_header("Content-Type", "application/octet-stream")
        req.add_header("Content-Length", str(length))
        if retention:
            req.add_header("Tie-Retention", retention)
        if owner:
            req.add_header("Tie-Owner", owner)
        self._prepare(req)
        with urllib.request.urlopen(req, context=self._ctx) as resp:
            return resp.read().decode().strip()

    def upload(self, path: str, retention: str = "", owner: str = "") -> UploadResult:
        result = UploadResult()
        try:
            self._upload(path, result, retention, owner)
        except OSError as e:
            result.error_msg += f"Error uploading {path}: {e}\n"
        return result

    def _upload(self, path: str, result: UploadResult, retention: str, owner: str) -> None:
        if os.path.isdir(path) and not os.path.islink(path):
            entries: list[DirEntry] = []
            for name in sorted(os.listdir(path)):  # match Go's os.ReadDir order
                child = os.path.join(path, name)
                self._upload(child, result, retention, owner)
                last = result.items[-1]
                entries.append(
                    DirEntry(hash=last.hash, filename=name, size=last.size, head=last.head)
                )
            manifest = tiedir.build_manifest(entries)
            h = self._put(manifest, len(manifest), retention, owner)
            result.items.append(
                UploadedItem(
                    hash=h,
                    filename=path,
                    media_type="inode/directory",
                    size=len(manifest),
                    head=manifest[: tiedir.MAGIC_NUMBER],
                )
            )
            return

        size = os.path.getsize(path)
        with open(path, "rb") as f:
            head = f.read(tiedir.MAGIC_NUMBER)
        media = filetype.media_type(head)
        with open(path, "rb") as f:
            h = self._put(f, size, retention, owner)
        result.items.append(
            UploadedItem(hash=h, filename=path, media_type=media, size=size, head=head)
        )

    def upload_bytes(self, data: bytes, retention: str = "", owner: str = "") -> str:
        """Upload a raw blob and return its content hash."""
        return self._put(data, len(data), retention, owner)

    # --- download ---

    def _get(self, source_hash: str) -> bytes:
        if not tiedir.is_hex_hash(source_hash):
            raise ValueError(f"invalid content hash {source_hash!r}")
        req = self._prepare(urllib.request.Request(f"{self.base}/{source_hash}"))
        with urllib.request.urlopen(req, context=self._ctx) as resp:
            return resp.read()

    def download_file(self, source_hash: str, dest: str, progress=None) -> None:
        """Download source_hash into dest, expanding directory manifests."""
        self._download(source_hash, dest, progress, 0)

    def _download(self, source_hash: str, dest: str, progress, depth: int) -> None:
        if depth > MAX_DEPTH:
            raise ValueError("directory nesting exceeds max depth")
        data = self._get(source_hash)
        if tiedir.looks_like_dir(data):
            for entry in tiedir.parse_manifest(data):
                self._download(
                    entry.hash, os.path.join(dest, entry.filename), progress, depth + 1
                )
            return
        os.makedirs(os.path.dirname(dest) or ".", exist_ok=True)
        with open(dest, "wb") as f:
            f.write(data)
        if progress is not None:
            progress(len(data))

    def download_bytes(self, source_hash: str) -> bytes:
        """Return a single blob's bytes, erroring if it is a directory."""
        data = self._get(source_hash)
        if tiedir.looks_like_dir(data):
            raise ValueError("source is a directory; expected file")
        return data

    def total_size(self, source_hash: str) -> int:
        return self._total_size(source_hash, 0)

    def _total_size(self, source_hash: str, depth: int) -> int:
        if depth > MAX_DEPTH:
            raise ValueError("directory nesting exceeds max depth")
        data = self._get(source_hash)
        if not tiedir.looks_like_dir(data):
            return len(data)
        total = 0
        for entry in tiedir.parse_manifest(data):
            if tiedir.is_dir_head(entry.head):
                total += self._total_size(entry.hash, depth + 1)
            else:
                total += entry.size
        return total

    # --- retention ---

    def get_retention(self, source_hash: str) -> dict:
        req = self._prepare(urllib.request.Request(f"{self.base}/retention/{source_hash}"))
        with urllib.request.urlopen(req, context=self._ctx) as resp:
            return json.loads(resp.read())

    def set_retention(self, source_hash: str, retention: str, owner: str = "") -> None:
        req = urllib.request.Request(
            f"{self.base}/retention/{source_hash}", data=b"", method="PUT"
        )
        if retention:
            req.add_header("Tie-Retention", retention)
        if owner:
            req.add_header("Tie-Owner", owner)
        self._prepare(req)
        with urllib.request.urlopen(req, context=self._ctx):
            pass
