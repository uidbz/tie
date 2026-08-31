"""TieClient: the full client facade over the triplestore and filehost.

Method names mirror client.TieClient (Go). The triplestore request envelopes are
built to match the JSON field casing of each api/*.go request type exactly
(some fields are PascalCase, some lowercase-tagged).
"""

from __future__ import annotations

import os
from collections.abc import Iterator
from dataclasses import dataclass, field
from datetime import datetime

from . import filetype as ft
from . import vocab
from .config import Config, FileHost, default_config, load_config
from .errors import NotFound, ServerError
from .filehost import FilehostClient, UploadResult
from .metadata_extract import Media, extract_media_metadata
from .transport import TripleStoreClient
from .vocab import FILE_URI_SCHEME, TAG_DATE_FORMAT, Relation, TieType


# ---------------------------------------------------------------------------
# Result and request value types
# ---------------------------------------------------------------------------


@dataclass
class Row:
    """A key plus its forward attributes (relation -> values)."""

    key: str
    attributes: dict[str, list[str]] = field(default_factory=dict)

    def values(self, relation: str) -> list[str]:
        return self.attributes.get(relation, [])

    def first(self, relation: str) -> str:
        v = self.attributes.get(relation)
        return v[0] if v else ""

    def has(self, relation: str, value: str) -> bool:
        return value in self.attributes.get(relation, [])


def _row_from_json(d: dict) -> Row:
    return Row(key=d.get("key", ""), attributes=d.get("attributes") or {})


@dataclass
class QuerySpec:
    terms: list[str] = field(default_factory=list)
    exclude: list[str] = field(default_factory=list)
    scope: str = ""
    missing_relation: str = ""
    filter: str = ""
    reverse: bool = False
    expand: bool = False
    offset: int = 0
    limit: int = 0
    sort_by: str = ""


@dataclass
class Update:
    key: str
    value1: str
    value2: str
    new_value2: str
    add_on_failure: bool = False


class Batch:
    """An ordered list of write ops against one collection."""

    def __init__(self, namespace: str, collection: str):
        self.namespace = namespace
        self.collection = collection
        self.ops: list[dict] = []

    def add(self, key: str, relation: str, value: str) -> None:
        self.ops.append({"op": "add", "key": key, "relation": relation, "values": [value]})

    def delete(self, key: str, relation: str, value: str) -> None:
        self.ops.append({"op": "delete", "key": key, "relation": relation, "values": [value]})

    def set(self, key: str, relation: str, values: list[str]) -> None:
        self.ops.append({"op": "set", "key": key, "relation": relation, "values": values})

    def update(self, u: Update) -> None:
        self.ops.append(
            {
                "op": "update",
                "key": u.key,
                "relation": u.value1,
                "values": [u.value2],
                "newValue": u.new_value2,
                "addOnFailure": u.add_on_failure,
            }
        )


@dataclass
class MediaRelation:
    from_hash: str
    relation: str
    to_hash: str


@dataclass
class VersionInfo:
    hash: str
    filename: str
    size: int
    tie_type: str
    date: datetime | None


@dataclass
class TaggedFile:
    hash: str
    filename: str
    size: int
    is_dir: bool
    is_archive: bool = False
    tie_type: str = ""


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _check(reply: dict) -> None:
    """Raise NotFound / ServerError for an unsuccessful reply."""
    if reply.get("Success"):
        return
    msg = reply.get("Message", "")
    if msg == "Key has no associated values":
        raise NotFound(msg)
    raise ServerError(msg or "request failed")


def _parse_tag_date(s: str) -> datetime | None:
    if not s:
        return None
    for fmt in (TAG_DATE_FORMAT, "%Y-%m-%d %H:%M:%S"):
        try:
            return datetime.strptime(s, fmt)
        except ValueError:
            continue
    return None


def _now_tag_date() -> str:
    return datetime.now().strftime(TAG_DATE_FORMAT)


# ---------------------------------------------------------------------------
# TieClient
# ---------------------------------------------------------------------------


class TieClient:
    def __init__(self, config: Config):
        self.config = config
        self._triplestore = TripleStoreClient(
            config.webservice, config.username, config.password, config.webservice_insecure
        )
        self._filehosts: dict[str, FilehostClient] = {}

    @classmethod
    def from_config_name(cls, name: str = "config") -> "TieClient":
        try:
            cfg = load_config(name)
        except FileNotFoundError:
            cfg = default_config()
        return cls(cfg)

    # --- collection / envelope helpers ---

    def _collection(self, collection: str = "") -> tuple[str, str]:
        return self.config.namespace, (collection or self.config.collection)

    def _envelope(self, request_id: str, collection: str = "") -> dict:
        ns, col = self._collection(collection)
        return {"Id": request_id, "Namespace": ns, "CollectionId": col}

    def filehost(self, host: FileHost) -> FilehostClient:
        key = host.url
        fc = self._filehosts.get(key)
        if fc is None:
            fc = FilehostClient(host)
            self._filehosts[key] = fc
        return fc

    def resolve_host(self, name: str) -> FileHost:
        return self.config.resolve_host(name)

    # --- triples ---

    def add(self, key: str, value1: str, value2: str, collection: str = "") -> None:
        body = self._envelope("Add", collection)
        body.update({"Key": key, "Value1": value1, "Value2": value2})
        _check(self._triplestore.run("Add", body))

    def delete(self, key: str, value1: str, value2: str, collection: str = "") -> None:
        body = self._envelope("Delete", collection)
        body.update({"Key": key, "Value1": value1, "Value2": value2})
        _check(self._triplestore.run("Delete", body))

    def set_values(self, key: str, relation: str, values: list[str], collection: str = "") -> None:
        body = self._envelope("Set", collection)
        body.update({"key": key, "relation": relation, "values": values})
        _check(self._triplestore.run("Set", body))

    def update(self, u: Update, collection: str = "") -> None:
        body = self._envelope("Update", collection)
        body.update(
            {
                "Key": u.key,
                "Value1": u.value1,
                "Value2": u.value2,
                "NewValue2": u.new_value2,
                "AddOnFailure": u.add_on_failure,
            }
        )
        _check(self._triplestore.run("Update", body))

    def sync(self, collection: str = "") -> None:
        self._triplestore.run("Sync", self._envelope("Sync", collection))

    def new_batch(self, collection: str = "") -> Batch:
        ns, col = self._collection(collection)
        return Batch(ns, col)

    def run_batch(self, batch: Batch) -> None:
        body = {
            "Id": "Batch",
            "batch": {
                "collection": {"Namespace": batch.namespace, "CollectionId": batch.collection},
                "ops": batch.ops,
            },
        }
        _check(self._triplestore.run("Batch", body))

    def query(self, spec: QuerySpec, collection: str = "") -> tuple[list[Row], int]:
        """Run a query. Raises NotFound when nothing matches."""
        body = self._envelope("Query", collection)
        body.update(
            {
                "terms": spec.terms,
                "exclude": spec.exclude,
                "scope": spec.scope,
                "missingRelation": spec.missing_relation,
                "filter": spec.filter,
                "reverse": spec.reverse,
                "expand": spec.expand,
                "sort": {"Offset": spec.offset, "Limit": spec.limit, "SortBy": spec.sort_by},
            }
        )
        reply = self._triplestore.run("Query", body)
        _check(reply)
        rows = [_row_from_json(r) for r in (reply.get("rows") or [])]
        return rows, reply.get("totalCount", 0)

    def expand(self, keys: list[str], filter: str = "", collection: str = "") -> list[Row]:
        body = self._envelope("Expand", collection)
        body.update({"keys": keys, "filter": filter})
        reply = self._triplestore.run("Expand", body)
        _check(reply)
        return [_row_from_json(r) for r in (reply.get("rows") or [])]

    def get(self, key: str, collection: str = "") -> Row:
        """Fetch one key's forward attributes. Raises NotFound if absent."""
        rows = self.expand([key], collection=collection)
        if not rows:
            raise NotFound("key has no associated values")
        return rows[0]

    def associated(self, key: str, match_value1: str = "", collection: str = "") -> dict:
        body = self._envelope("Associated", collection)
        body.update({"Key": key, "MatchValue1": match_value1})
        reply = self._triplestore.run("Associated", body)
        _check(reply)
        return reply.get("Result") or {}

    def dump(self, collection: str = "") -> Iterator[tuple[str, str, str]]:
        """Yield every forward triple (key, value1, value2) as NDJSON stream."""
        body = self._envelope("Dump", collection)
        for t in self._triplestore.run_stream("Dump", body):
            yield t.get("Key", ""), t.get("Value1", ""), t.get("Value2", "")

    def drop_collection(self, collection: str = "") -> None:
        _check(self._triplestore.run("Drop", self._envelope("Drop", collection)))

    def restore(self, triples: list[tuple[str, str, str]], collection: str = "") -> None:
        if not triples:
            return
        b = self.new_batch(collection)
        for k, v1, v2 in triples:
            b.add(k, v1, v2)
        self.run_batch(b)

    def exists(self, key: str) -> bool:
        try:
            self.get(key)
            return True
        except NotFound:
            return False

    # --- files ---

    def upload(self, host_name: str, path: str) -> UploadResult:
        return self.filehost(self.resolve_host(host_name)).upload(path)

    def upload_to(self, host: FileHost, path: str) -> UploadResult:
        return self.filehost(host).upload(path)

    def download(self, host_name: str, source_hash: str, dest: str, progress=None) -> None:
        self.filehost(self.resolve_host(host_name)).download_file(source_hash, dest, progress)

    def download_from(self, host: FileHost, source_hash: str, dest: str, progress=None) -> None:
        self.filehost(host).download_file(source_hash, dest, progress)

    def download_size(self, host: FileHost, source_hash: str) -> int:
        return self.filehost(host).total_size(source_hash)

    def new_dir_uid(self) -> str:
        """Mint a DirUID: 64-hex of 32 random bytes (content-hash shape)."""
        return os.urandom(32).hex()
