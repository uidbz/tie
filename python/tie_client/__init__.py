"""Pure-Python client for the tie triple-store and content-addressed filehost."""

from __future__ import annotations

from . import highlevel  # noqa: F401  (binds high-level methods onto TieClient)
from .client import (
    Batch,
    MediaRelation,
    QuerySpec,
    Row,
    TaggedFile,
    TieClient,
    Update,
    VersionInfo,
)
from .config import (
    CollectionEntry,
    Config,
    FileHost,
    default_config,
    load_config,
    save_config,
)
from .errors import NotFound, ServerError, TieError, Unauthorized
from .filehost import UploadedItem, UploadResult
from .vocab import FILE_URI_SCHEME, VALUE_TYPES, Relation, TieType, ValueType

__all__ = [
    "TieClient",
    "Config",
    "CollectionEntry",
    "FileHost",
    "QuerySpec",
    "Row",
    "Update",
    "Batch",
    "TaggedFile",
    "VersionInfo",
    "MediaRelation",
    "UploadResult",
    "UploadedItem",
    "Relation",
    "TieType",
    "ValueType",
    "VALUE_TYPES",
    "FILE_URI_SCHEME",
    "NotFound",
    "Unauthorized",
    "ServerError",
    "TieError",
    "default_config",
    "load_config",
    "save_config",
]
