"""High-level operations attached to TieClient.

Ports the tag/dir/import/version/favorite/cotag logic from client/tag.go,
client/versions.go, client/favorites.go and client/cotags.go. These are
defined as free functions and bound onto TieClient at import time so the whole
API lives on one facade object (mirroring the Go client).
"""

from __future__ import annotations

import os
import posixpath
import zipfile

from . import filetype as ft
from .client import (
    Batch,
    MediaRelation,
    QuerySpec,
    Row,
    TaggedFile,
    TieClient,
    Update,
    VersionInfo,
    _now_tag_date,
    _parse_tag_date,
)
from .errors import NotFound
from .metadata_extract import Media, extract_media_metadata
from .vocab import (
    VALUE_TYPES,
    ARCHIVE_TYPES,
    DIR_TYPES,
    FILE_URI_SCHEME,
    Relation,
    TieType,
    archive_type_of,
    contains_archive_type,
    is_archive_type,
    is_value_type,
)

FLUSH_THRESHOLD = 1000
TAG_BATCH_CHUNK = 1000
TYPE_REGISTRY_SUBJECT = "types"

# Extension -> category, for refining a zip archive to a media-specific type.
_EXT_CATEGORY = {
    **{e: "image" for e in (".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic")},
    **{e: "audio" for e in (".mp3", ".flac", ".ogg", ".wav", ".m4a", ".aac", ".mid")},
    **{e: "video" for e in (".mp4", ".mkv", ".webm", ".mov", ".avi", ".wmv", ".flv", ".m4v", ".3gp")},
    **{e: "document" for e in (".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".odt")},
}

_CATEGORY_ARCHIVE = {
    "image": TieType.IMAGE_ARCHIVE,
    "audio": TieType.AUDIO_ARCHIVE,
    "video": TieType.VIDEO_ARCHIVE,
    "document": TieType.DOCUMENT_ARCHIVE,
}


# ---------------------------------------------------------------------------
# tie-type detection
# ---------------------------------------------------------------------------


def get_tie_type_from_path(path: str) -> str:
    """Sniff a file's tie-type, refining archives to media-specific types."""
    try:
        with open(path, "rb") as f:
            head = f.read(261)
    except OSError:
        return TieType.UNKNOWN_FILE
    t = ft.tie_type_of(head)
    if t == TieType.ARCHIVE_FILE and zipfile.is_zipfile(path):
        return _refine_archive(path)
    return t


def _refine_archive(path: str) -> str:
    counts: dict[str, int] = {}
    try:
        with zipfile.ZipFile(path) as z:
            for name in z.namelist():
                if name.endswith("/"):
                    continue
                ext = os.path.splitext(name)[1].lower()
                cat = _EXT_CATEGORY.get(ext)
                if cat:
                    counts[cat] = counts.get(cat, 0) + 1
    except (zipfile.BadZipFile, OSError):
        return TieType.ARCHIVE_FILE
    if not counts:
        return TieType.ARCHIVE_FILE
    modal = max(counts, key=lambda c: counts[c])
    return _CATEGORY_ARCHIVE.get(modal, TieType.ARCHIVE_FILE)


def _apply_archive_override(sniffed: str, forced: str) -> str:
    if forced and is_archive_type(forced) and is_archive_type(sniffed):
        return forced
    return sniffed


TieClient.get_tie_type_from_path = staticmethod(get_tie_type_from_path)


# ---------------------------------------------------------------------------
# tag ops (batch builders)
# ---------------------------------------------------------------------------


def _append_tag_ops(batch: Batch, hash: str, filename: str, size: int, media_type: str,
                    tie_type: str, tags: list[str], directory: str, is_dir: bool,
                    meta: Media | None) -> None:
    base = os.path.basename(filename)
    root, _ = os.path.splitext(base)
    batch.add(hash, Relation.FILENAME, base)
    batch.add(hash, Relation.NAME, root)
    batch.add(hash, Relation.MEDIA_TYPE, media_type)
    batch.add(hash, Relation.TIE_TYPE, tie_type)
    batch.add(hash, Relation.FILESIZE, str(size))
    batch.set(hash, Relation.TAG_DATE, [_now_tag_date()])
    for tag in tags:
        if not tag:
            continue
        if tag[0] == "-":
            batch.delete(hash, Relation.TAG, tag[1:])
        else:
            batch.add(hash, Relation.TAG, tag)
            batch.add(Relation.TAGS, Relation.ALL, tag)
    if meta:
        if meta.title:
            batch.add(hash, Relation.TITLE, meta.title)
        if meta.artist:
            batch.add(hash, Relation.ARTIST, meta.artist)
        if meta.album:
            batch.add(hash, Relation.ALBUM, meta.album)
        if meta.year:
            batch.add(hash, Relation.YEAR, str(meta.year))
        if meta.track:
            batch.add(hash, Relation.TRACK, str(meta.track))
    batch.add(hash, Relation.TIE_TYPE, TieType.DIRECTORY if is_dir else TieType.FILE)
    if directory:
        batch.add(hash, Relation.PARENT, directory)


def _append_tag_dir_ops(batch: Batch, uid: str, dirname: str, size: int, tags: list[str]) -> None:
    batch.add(uid, Relation.FILENAME, dirname)
    batch.add(uid, Relation.NAME, dirname)
    batch.add(uid, Relation.FILESIZE, str(size))
    batch.set(uid, Relation.TAG_DATE, [_now_tag_date()])
    for tag in tags:
        if not tag:
            continue
        if tag[0] == "-":
            batch.delete(uid, Relation.TAG, tag[1:])
        else:
            batch.add(uid, Relation.TAG, tag)
            batch.add(Relation.TAGS, Relation.ALL, tag)


# ---------------------------------------------------------------------------
# directory / path tree
# ---------------------------------------------------------------------------


def dir_uid_from_path(self: TieClient, path: str) -> str:
    if not path.startswith(FILE_URI_SCHEME):
        path = FILE_URI_SCHEME + path
    try:
        rows, _ = self.query(QuerySpec(terms=[path], reverse=True, filter=Relation.PATH))
    except NotFound:
        return ""
    if len(rows) > 1:
        import sys
        print(
            f"warning: {len(rows)} UIDs found for path {path!r}; expected 1 — using {rows[0].key}",
            file=sys.stderr,
        )
    return rows[0].key if rows else ""


def create_root_dir(self: TieClient) -> None:
    rootpath = FILE_URI_SCHEME + "/"
    uid = self.dir_uid_from_path(rootpath)
    if uid:
        raise ValueError("Root dir already exists, with UID: " + uid)
    uid = self.new_dir_uid()
    b = self.new_batch()
    b.add(uid, Relation.PARENT, uid)
    b.add(uid, Relation.PATH, rootpath)
    b.add(uid, Relation.TIE_TYPE, TieType.DIRECTORY)
    self.run_batch(b)


def _tie_dir_ancestors(p: str) -> list[str]:
    p = p[len(FILE_URI_SCHEME):] if p.startswith(FILE_URI_SCHEME) else p
    if not p.startswith("/"):
        p = "/" + p
    p = posixpath.normpath(p)
    if p == "/":
        return []
    segments = p.lstrip("/").split("/")
    ancestors = []
    prefix = FILE_URI_SCHEME
    for seg in segments:
        prefix = prefix + "/" + seg
        ancestors.append(prefix)
    return ancestors


def mk_tie_dir(self: TieClient, path: str) -> str:
    if not path.startswith(FILE_URI_SCHEME):
        path = FILE_URI_SCHEME + path
    uid = self.dir_uid_from_path(path)
    if uid:
        raise FileExistsError("Cannot create directory '" + path + "': Directory exists")
    # parent path (posix dirname on the scheme-stripped path)
    stripped = path[len(FILE_URI_SCHEME):]
    parent_stripped = posixpath.dirname(stripped)
    parent_path = FILE_URI_SCHEME + parent_stripped
    if parent_path == FILE_URI_SCHEME:
        parent_path = FILE_URI_SCHEME + "/"
    uid = self.new_dir_uid()
    parent_uid = self.dir_uid_from_path(parent_path)
    b = self.new_batch()
    b.add(uid, Relation.PARENT, parent_uid)
    b.add(uid, Relation.PATH, path)
    b.add(uid, Relation.TIE_TYPE, TieType.DIRECTORY)
    self.run_batch(b)
    return uid


def mk_tie_dir_all(self: TieClient, dir_path: str) -> str:
    try:
        self.create_root_dir()
    except ValueError as e:
        if not str(e).startswith("Root dir already exists"):
            raise
    uid = ""
    for ancestor in _tie_dir_ancestors(dir_path):
        try:
            uid = self.mk_tie_dir(ancestor)
        except FileExistsError:
            uid = self.dir_uid_from_path(ancestor)
    return uid


TieClient.dir_uid_from_path = dir_uid_from_path
TieClient.create_root_dir = create_root_dir
TieClient.mk_tie_dir = mk_tie_dir
TieClient.mk_tie_dir_all = mk_tie_dir_all


# ---------------------------------------------------------------------------
# directory reading and dir-types
# ---------------------------------------------------------------------------

from dataclasses import dataclass, field  # noqa: E402


@dataclass
class SubDirectory:
    uid: str
    paths: list[str] = field(default_factory=list)
    dir_types: list[str] = field(default_factory=list)


@dataclass
class DirFile:
    filename: str
    uid: str
    tie_type: str
    media_type: str
    size: int
    tag_date: object = None


@dataclass
class ArchiveEntry:
    filename: str
    hash: str
    tie_type: str
    size: int
    tag_date: object = None


@dataclass
class Directory:
    uid: str
    paths: list[str] = field(default_factory=list)
    parent_uids: list[str] = field(default_factory=list)
    sub_dirs: list[SubDirectory] = field(default_factory=list)
    files: list[DirFile] = field(default_factory=list)
    archives: list[ArchiveEntry] = field(default_factory=list)


def read_tie_dir(self: TieClient, uid: str) -> Directory:
    self_row = self.get(uid)
    d = Directory(uid=uid, paths=self_row.values(Relation.PATH),
                  parent_uids=list(self_row.values(Relation.PARENT)))
    try:
        rows, _ = self.query(QuerySpec(terms=[uid], reverse=True, expand=True))
    except NotFound:
        return d
    for row in rows:
        types = row.attributes.get(Relation.TIE_TYPE, [])
        if TieType.DIRECTORY in types:
            d.sub_dirs.append(SubDirectory(uid=row.key, paths=row.values(Relation.PATH),
                                           dir_types=list(types)))
        elif contains_archive_type(types):
            size = int(row.first(Relation.FILESIZE) or 0)
            filename = row.first(Relation.FILENAME) or row.key
            d.archives.append(ArchiveEntry(filename=filename, hash=row.key,
                                           tie_type=archive_type_of(types), size=size,
                                           tag_date=_parse_tag_date(row.first(Relation.TAG_DATE))))
        else:
            size = int(row.first(Relation.FILESIZE) or 0)
            filename = row.first(Relation.FILENAME) or row.key
            d.files.append(DirFile(filename=filename, uid=row.key,
                                   tie_type=", ".join(types), media_type=row.first(Relation.MEDIA_TYPE),
                                   size=size, tag_date=_parse_tag_date(row.first(Relation.TAG_DATE))))
    d.files.sort(key=lambda f: (f.filename, f.uid))
    d.archives.sort(key=lambda a: (a.filename, a.hash))
    return d


def set_dir_type(self: TieClient, uid: str, dir_type: str) -> None:
    self.add(uid, Relation.TIE_TYPE, dir_type)
    if dir_type != TieType.DIRECTORY:
        self.add(TYPE_REGISTRY_SUBJECT, Relation.ALL, dir_type)


def get_dir_type(self: TieClient, uid: str) -> list[str]:
    try:
        row = self.get(uid)
    except NotFound:
        return []
    labels = [t for t in row.values(Relation.TIE_TYPE) if t != TieType.DIRECTORY]
    labels.sort()
    return labels


def set_dir_types(self: TieClient, uid: str, new_labels: list[str]) -> None:
    current = set(self.get_dir_type(uid))
    want = {t.strip() for t in new_labels if t.strip() and t.strip() != TieType.DIRECTORY}
    batch = self.new_batch()
    changed = False
    for t in want - current:
        batch.add(uid, Relation.TIE_TYPE, t)
        batch.add(TYPE_REGISTRY_SUBJECT, Relation.ALL, t)
        changed = True
    for t in current - want:
        batch.delete(uid, Relation.TIE_TYPE, t)
        changed = True
    if changed:
        self.run_batch(batch)
        self.sync()


def list_dir_types(self: TieClient) -> list[str]:
    labels = set(DIR_TYPES)
    try:
        rows, _ = self.query(QuerySpec(terms=[TYPE_REGISTRY_SUBJECT], filter=Relation.ALL))
        for row in rows:
            labels.update(row.values(Relation.ALL))
    except NotFound:
        pass
    return sorted(labels)


def _collect_descendant_dirs(self: TieClient, root: str) -> list[SubDirectory]:
    out: list[SubDirectory] = []
    queue = [root]
    while queue:
        cur = queue.pop(0)
        d = self.read_tie_dir(cur)
        for sub in d.sub_dirs:
            out.append(sub)
            queue.append(sub.uid)
    return out


def rename_file(self: TieClient, hash: str, old_parent: str, new_parent: str, new_name: str) -> None:
    try:
        row = self.get(hash)
    except NotFound:
        row = Row(key=hash)
    old_filename = row.first(Relation.FILENAME)
    old_name = row.first(Relation.NAME)
    batch = self.new_batch()
    if new_name != old_filename:
        new_base = os.path.splitext(new_name)[0]
        fn = Update(hash, Relation.FILENAME, old_filename, new_name, add_on_failure=True)
        batch.update(fn)
        nm = Update(hash, Relation.NAME, old_name, new_base, add_on_failure=True)
        batch.update(nm)
    if new_parent != old_parent:
        batch.delete(hash, Relation.PARENT, old_parent)
        batch.add(hash, Relation.PARENT, new_parent)
    self.run_batch(batch)
    self.sync()


def rename_dir(self: TieClient, uid: str, old_path: str, new_path: str,
               old_parent: str, new_parent: str) -> None:
    descendants = self._collect_descendant_dirs(uid)
    batch = self.new_batch()
    batch.update(Update(uid, Relation.PATH, old_path, new_path, add_on_failure=True))
    for d in descendants:
        for p in d.paths:
            if not p.startswith(old_path):
                continue
            np = new_path + p[len(old_path):]
            batch.update(Update(d.uid, Relation.PATH, p, np, add_on_failure=True))
    if new_parent != old_parent:
        batch.delete(uid, Relation.PARENT, old_parent)
        batch.add(uid, Relation.PARENT, new_parent)
    self.run_batch(batch)
    self.sync()


TieClient.read_tie_dir = read_tie_dir
TieClient.set_dir_type = set_dir_type
TieClient.get_dir_type = get_dir_type
TieClient.set_dir_types = set_dir_types
TieClient.list_dir_types = list_dir_types
TieClient._collect_descendant_dirs = _collect_descendant_dirs
TieClient.rename_file = rename_file
TieClient.rename_dir = rename_dir


# ---------------------------------------------------------------------------
# value types
# ---------------------------------------------------------------------------


VALUE_TYPE_REGISTRY_SUBJECT = "value-types"
"""Subject of the per-collection value-type registry (mirrors the Go
client's private const; its attributes *are* the declared schema)."""


def set_value_types(self: TieClient, types: dict[str, str]) -> None:
    """Declare the value type of each named relation, merging into whatever the
    collection already holds.

    Declaring "" or VALUE_TYPES.STRING clears a relation's entry, since an
    absent declaration already means string. Every type is checked before
    anything is written, so a mapping containing one unknown type declares
    nothing. Declarations replace rather than accumulate: one batch of set ops,
    mirroring client.SetValueTypes.
    """
    if not types:
        return
    for relation, t in types.items():
        if not relation:
            raise ValueError("SetValueType: relation must not be empty")
        if t and t not in VALUE_TYPES:
            raise ValueError(f"SetValueType: unknown value type {t!r} for relation {relation!r}")
    batch = self.new_batch()
    for relation, t in types.items():
        if t is None or t == "" or t == ValueType.STRING:
            batch.set(VALUE_TYPE_REGISTRY_SUBJECT, relation, [])
        else:
            batch.set(VALUE_TYPE_REGISTRY_SUBJECT, relation, [t])
    self.run_batch(batch)
    self.sync()


def value_types(self: TieClient) -> dict[str, str]:
    """Return every value type declared in the collection, keyed by relation.

    One round trip for a whole collection's schema; fetch it once per
    collection and cache it. A collection with no declarations yields an empty
    dict. Relations declared with a type this client does not know are omitted
    rather than reported (they read as string, the default anyway).
    """
    try:
        row = self.get(VALUE_TYPE_REGISTRY_SUBJECT)
    except NotFound:
        return {}
    return {relation: row.first(relation) for relation in row.attributes
            if is_value_type(row.first(relation))}


TieClient.set_value_types = set_value_types
TieClient.value_types = value_types


# ---------------------------------------------------------------------------
# tag management
# ---------------------------------------------------------------------------


def _tagged_file_from(row: Row) -> TaggedFile:
    is_dir = row.has(Relation.TIE_TYPE, TieType.DIRECTORY)
    types = row.attributes.get(Relation.TIE_TYPE, [])
    is_archive = not is_dir and contains_archive_type(types)
    filename = row.first(Relation.FILENAME)
    if filename and ("/" in filename or "\\" in filename):
        filename = os.path.basename(filename)
    if not filename and is_dir:
        paths = row.values(Relation.PATH)
        if paths:
            p = paths[0]
            if p.startswith(FILE_URI_SCHEME):
                p = p[len(FILE_URI_SCHEME):]
            p = p.rstrip("/")
            filename = p.rsplit("/", 1)[-1] if "/" in p else p
    if not filename:
        filename = row.key
    size = int(row.first(Relation.FILESIZE) or 0)
    tf = TaggedFile(hash=row.key, filename=filename, size=size, is_dir=is_dir, is_archive=is_archive)
    if is_archive:
        tf.tie_type = archive_type_of(types)
    return tf


def list_tags(self: TieClient, offset: int = 0, limit: int = 0) -> tuple[list[str], int]:
    try:
        rows, total = self.query(QuerySpec(terms=[Relation.TAGS], filter=Relation.ALL,
                                           offset=offset, limit=limit))
    except NotFound:
        return [], 0
    tags: list[str] = []
    for row in rows:
        tags.extend(row.values(Relation.ALL))
    return tags, total


def register_tag(self: TieClient, tag: str) -> None:
    tag = tag.strip()
    if not tag:
        raise ValueError("RegisterTag: tag must not be empty")
    self.add(Relation.TAGS, Relation.ALL, tag)
    self.sync()


def _tagged_subjects(self: TieClient, tag: str) -> list[str]:
    try:
        rows, _ = self.query(QuerySpec(terms=[tag], filter=Relation.TAG, reverse=True, limit=-1))
    except NotFound:
        return []
    return [row.key for row in rows]


def delete_tag(self: TieClient, tag: str) -> int:
    tag = tag.strip()
    if not tag:
        raise ValueError("DeleteTag: tag must not be empty")
    subjects = self._tagged_subjects(tag)
    for i in range(0, len(subjects), TAG_BATCH_CHUNK):
        b = self.new_batch()
        for subject in subjects[i:i + TAG_BATCH_CHUNK]:
            b.delete(subject, Relation.TAG, tag)
        self.run_batch(b)
    reg = self.new_batch()
    reg.delete(Relation.TAGS, Relation.ALL, tag)
    self.run_batch(reg)
    self.sync()
    return len(subjects)


def rename_tag(self: TieClient, old_tag: str, new_tag: str) -> int:
    old_tag, new_tag = old_tag.strip(), new_tag.strip()
    if not old_tag or not new_tag:
        raise ValueError("RenameTag: tags must not be empty")
    if old_tag == new_tag:
        return 0
    subjects = self._tagged_subjects(old_tag)
    for i in range(0, len(subjects), TAG_BATCH_CHUNK):
        b = self.new_batch()
        for subject in subjects[i:i + TAG_BATCH_CHUNK]:
            b.delete(subject, Relation.TAG, old_tag)
            b.add(subject, Relation.TAG, new_tag)
        self.run_batch(b)
    reg = self.new_batch()
    reg.delete(Relation.TAGS, Relation.ALL, old_tag)
    reg.add(Relation.TAGS, Relation.ALL, new_tag)
    self.run_batch(reg)
    self.sync()
    return len(subjects)


def get_tags(self: TieClient, hash: str) -> list[str]:
    try:
        row = self.get(hash)
    except NotFound:
        return []
    tags = row.values(Relation.TAG)
    return sorted(tags)


def set_tags(self: TieClient, hash: str, new_tags: list[str]) -> None:
    current = set(self.get_tags(hash))
    want = {t.strip() for t in new_tags if t.strip()}
    batch = self.new_batch()
    changed = False
    for t in want - current:
        batch.add(hash, Relation.TAG, t)
        batch.add(Relation.TAGS, Relation.ALL, t)
        changed = True
    for t in current - want:
        batch.delete(hash, Relation.TAG, t)
        changed = True
    if changed:
        self.run_batch(batch)
        self.sync()


def _files_of_type(self: TieClient, scope: str, offset: int, limit: int) -> tuple[list[TaggedFile], int]:
    try:
        rows, total = self.query(QuerySpec(terms=[scope], filter=Relation.TIE_TYPE,
                                           reverse=True, expand=True, offset=offset, limit=limit))
    except NotFound:
        return [], 0
    return [_tagged_file_from(r) for r in rows], total


def files_with_tags(self: TieClient, scope: str, include: list[str], exclude: list[str],
                    offset: int = 0, limit: int = -1) -> tuple[list[TaggedFile], int]:
    if not include:
        if not scope:
            return [], 0
        return self._files_of_type(scope, offset, limit)
    try:
        rows, total = self.query(QuerySpec(terms=include, exclude=exclude or [], scope=scope,
                                           filter=Relation.TAG, reverse=True, expand=True,
                                           offset=offset, limit=limit))
    except NotFound:
        return [], 0
    return [_tagged_file_from(r) for r in rows], total


def _untagged_of_type(self: TieClient, scope: str, offset: int, limit: int) -> tuple[list[TaggedFile], int]:
    try:
        rows, total = self.query(QuerySpec(terms=[scope], filter=Relation.TIE_TYPE,
                                           missing_relation=Relation.TAG, reverse=True,
                                           expand=True, offset=offset, limit=limit))
    except NotFound:
        return [], 0
    return [_tagged_file_from(r) for r in rows], total


def untagged_files(self: TieClient, scope: str, offset: int = 0, limit: int = -1) -> tuple[list[TaggedFile], int]:
    if scope:
        return self._untagged_of_type(scope, offset, limit)
    untagged: list[TaggedFile] = []
    seen: set[str] = set()
    for s in (TieType.FILE, TieType.DIRECTORY):
        files, _ = self._untagged_of_type(s, 0, -1)
        for f in files:
            if f.hash in seen:
                continue
            seen.add(f.hash)
            untagged.append(f)
    untagged.sort(key=lambda f: (f.filename, f.hash))
    total = len(untagged)
    if offset < 0:
        offset = 0
    offset = min(offset, total)
    untagged = untagged[offset:]
    if limit > 0 and limit < len(untagged):
        untagged = untagged[:limit]
    return untagged, total


TieClient.list_tags = list_tags
TieClient.register_tag = register_tag
TieClient._tagged_subjects = _tagged_subjects
TieClient.delete_tag = delete_tag
TieClient.rename_tag = rename_tag
TieClient.get_tags = get_tags
TieClient.set_tags = set_tags
TieClient._files_of_type = _files_of_type
TieClient.files_with_tags = files_with_tags
TieClient._untagged_of_type = _untagged_of_type
TieClient.untagged_files = untagged_files


# ---------------------------------------------------------------------------
# import
# ---------------------------------------------------------------------------


def _sanitize_path_segment(s: str) -> str:
    out = []
    for ch in s:
        if ch in ("/", "\\"):
            out.append(" ")
        elif ord(ch) < 0x20:
            continue
        else:
            out.append(ch)
    r = "".join(out).strip()
    return "" if r in (".", "..") else r


def _render_dest_template(tmpl: str, m: Media) -> str:
    def value(name: str):
        if name == "artist":
            return _sanitize_path_segment(m.artist), True
        if name == "album":
            return _sanitize_path_segment(m.album), True
        if name == "title":
            return _sanitize_path_segment(m.title), True
        if name == "year":
            return ("" if m.year == 0 else str(m.year)), True
        if name == "track":
            return ("" if m.track == 0 else str(m.track)), True
        return "", False

    out = []
    i = 0
    while i < len(tmpl):
        c = tmpl[i]
        if c != "{":
            out.append(c)
            i += 1
            continue
        end = tmpl.find("}", i)
        if end < 0:
            raise ValueError(f"unterminated '{{' in import destination template {tmpl!r}")
        name = tmpl[i + 1:end]
        v, ok = value(name)
        if not ok:
            raise ValueError(f"unknown variable {{{name}}} in template {tmpl!r}")
        if v == "":
            raise ValueError(f"empty value for {{{name}}}")
        out.append(v)
        i = end + 1
    return "".join(out)


def _aggregate_metadata(items: list[Media]) -> Media:
    def mode_str(get):
        counts: dict[str, int] = {}
        for it in items:
            v = get(it)
            if v:
                counts[v] = counts.get(v, 0) + 1
        return max(counts, key=lambda k: counts[k]) if counts else ""

    def mode_int(get):
        counts: dict[int, int] = {}
        for it in items:
            v = get(it)
            if v:
                counts[v] = counts.get(v, 0) + 1
        return max(counts, key=lambda k: counts[k]) if counts else 0

    return Media(
        artist=mode_str(lambda m: m.artist),
        album=mode_str(lambda m: m.album),
        title=mode_str(lambda m: m.title),
        year=mode_int(lambda m: m.year),
        track=mode_int(lambda m: m.track),
    )


def _import_root_path(self: TieClient, dir: str, dest: str, dir_type: str, meta: list[Media]) -> str:
    if dest:
        return FILE_URI_SCHEME + "/" + dest.replace("\\", "/").lstrip("/")
    tmpl = self.config.import_dest.get(dir_type)
    if tmpl:
        try:
            rendered = _render_dest_template(tmpl, _aggregate_metadata(meta))
            return FILE_URI_SCHEME + "/" + rendered.replace("\\", "/").lstrip("/")
        except ValueError as e:
            print("import destination template not applied, using source path:", e)
    abs_dir = os.path.abspath(dir)
    return FILE_URI_SCHEME + abs_dir.replace("\\", "/")


def _media_dict(m: Media):
    return m if (m.title or m.artist or m.album or m.year or m.track) else None


def tag_one(self: TieClient, hash: str, file: str, size: int, media_type: str, tie_type: str,
            tags: list[str], directory: str, is_dir: bool, meta: Media | None,
            collection: str = "") -> None:
    batch = self.new_batch(collection)
    _append_tag_ops(batch, hash, file, size, media_type, tie_type, tags, directory, is_dir, meta)
    self.run_batch(batch)


def import_file(self: TieClient, file: str, host, collection: str = "", tags: list[str] | None = None,
                directory: str = "", forced_archive: str = "") -> None:
    print("Importing:", file)
    tags = tags or []
    file_type = _apply_archive_override(get_tie_type_from_path(file), forced_archive)
    size = os.path.getsize(file)
    result = self.filehost(host).upload(file)
    if result.error_msg:
        raise RuntimeError(f"Error uploading: {file}\n{result.error_msg}")
    item = result.items[-1]
    meta = _media_dict(extract_media_metadata(file))
    tag_one(self, item.hash, file, size, item.media_type, file_type, tags,
            directory, os.path.isdir(file), meta, collection)


def _parent_count(self: TieClient, hash: str) -> int:
    try:
        row = self.get(hash)
    except NotFound:
        return 0
    return len(row.values(Relation.PARENT))


def _detach_child(self: TieClient, collection: str, hash: str, from_dir: str) -> None:
    b = self.new_batch(collection)
    b.delete(hash, Relation.PARENT, from_dir)
    self.run_batch(b)
    self.sync()
    if self._parent_count(hash) > 0:
        return
    try:
        row = self.get(hash)
    except NotFound:
        return
    meta = self.new_batch(collection)
    for relation, values in row.attributes.items():
        for v in values:
            meta.delete(hash, relation, v)
    self.run_batch(meta)
    self.sync()


def _file_children(self: TieClient, uid: str) -> list[dict]:
    try:
        rows, _ = self.query(QuerySpec(terms=[uid], reverse=True, filter=Relation.PARENT, expand=True))
    except NotFound:
        return []
    out = []
    for row in rows:
        if row.has(Relation.TIE_TYPE, TieType.DIRECTORY):
            continue
        names = sorted(row.values(Relation.FILENAME))
        if not names:
            continue
        out.append({"hash": row.key, "filename": names[0],
                    "tag_date": _parse_tag_date(row.first(Relation.TAG_DATE))})
    return out


def _retention_drops(versions: list[dict], keep: int) -> list[dict]:
    from datetime import datetime
    if keep <= 0 or len(versions) <= keep:
        return []
    def key(v):
        return (v["tag_date"] or datetime.min, v["hash"])
    ordered = sorted(versions, key=key)
    return ordered[:len(ordered) - keep]


def _reconcile_dir(self: TieClient, collection: str, uid: str, want: set[str] | None,
                   prev_versions: int) -> None:
    import sys
    children = self._file_children(uid)
    want = want or set()
    superseded = [c for c in children if c["hash"] not in want]
    for c in superseded:
        print(f"reconcile: versioning superseded {c['filename']} ({c['hash']}) from {uid}",
              file=sys.stderr)
        _supersede_to_prev(self, collection, uid, c["filename"], c["hash"])


def import_dir(self: TieClient, dir: str, host, collection: str = "", dir_type: str = "",
               tags: list[str] | None = None, dest: str = "", forced_archive: str = "") -> None:
    tags = tags or []
    result = self.filehost(host).upload(dir)
    if result.error_msg:
        raise RuntimeError(f"Error uploading: {dir}\n{result.error_msg}")

    file_meta: dict[str, Media] = {}
    all_meta: list[Media] = []
    for x in result.items:
        if x.media_type == "inode/directory":
            continue
        m = extract_media_metadata(x.filename)
        file_meta[x.filename] = m
        all_meta.append(m)

    root_path = self._import_root_path(dir, dest, dir_type, all_meta)
    dir_cache: dict[str, str] = {}
    want: dict[str, set[str]] = {}

    def dir_uid(rel_dir: str) -> str:
        if rel_dir in dir_cache:
            return dir_cache[rel_dir]
        vpath = root_path
        if rel_dir != ".":
            vpath += "/" + rel_dir.replace("\\", "/")
        uid = self.mk_tie_dir_all(vpath)
        dir_cache[rel_dir] = uid
        return uid

    batch = self.new_batch(collection)

    def flush():
        nonlocal batch
        if not batch.ops:
            return
        self.run_batch(batch)
        batch = self.new_batch(collection)

    for x in result.items:
        rel = os.path.relpath(x.filename, dir)
        if x.media_type == "inode/directory":
            uid = dir_uid(rel)
            if rel == ".":
                dirname = os.path.basename(root_path[len(FILE_URI_SCHEME):].rstrip("/"))
            else:
                dirname = os.path.basename(rel)
            _append_tag_dir_ops(batch, uid, dirname, x.size, tags)
            batch.add(uid, Relation.TIEDIR_HASH, x.hash)
            if len(batch.ops) >= FLUSH_THRESHOLD:
                flush()
            continue
        parent = dir_uid(os.path.dirname(rel))
        file_type = _apply_archive_override(get_tie_type_from_path(x.filename), forced_archive)
        print("tagging", x.hash)
        _append_tag_ops(batch, x.hash, x.filename, x.size, x.media_type, file_type, tags,
                        parent, False, _media_dict(file_meta.get(x.filename, Media())))
        if len(batch.ops) >= FLUSH_THRESHOLD:
            flush()
        want.setdefault(parent, set()).add(x.hash)

    flush()
    root_uid = dir_uid(".")
    for rel_dir, uid in dir_cache.items():
        _reconcile_dir(self, collection, uid, want.get(uid), self.config.prev_versions)
    self.set_dir_type(root_uid, dir_type)


TieClient._import_root_path = _import_root_path
TieClient.tag_one = tag_one
TieClient.import_file = import_file
TieClient._parent_count = _parent_count
TieClient._detach_child = _detach_child
TieClient._file_children = _file_children
TieClient.import_dir = import_dir


# ---------------------------------------------------------------------------
# versions (isolated <Collection>_prev history collection)
# ---------------------------------------------------------------------------


def _version_loc_key(parent_uid: str, name: str) -> str:
    return parent_uid + "\x00" + name


def _supersede_to_prev(self: TieClient, main_collection: str, parent_uid: str,
                       name: str, old_hash: str) -> None:
    if self.config.prev_versions <= 0:
        self._detach_child(main_collection, old_hash, parent_uid)
        return
    try:
        old_row = self.get(old_hash, collection=main_collection)
    except NotFound:
        old_row = Row(key=old_hash)
    prev_col = self.config.prev_collection_for(main_collection)
    loc_key = _version_loc_key(parent_uid, name)

    b = self.new_batch(prev_col)
    for relation, values in old_row.attributes.items():
        if relation == Relation.PARENT:
            continue
        for v in values:
            b.add(old_hash, relation, v)
    b.add(old_hash, Relation.VERSION_OF, loc_key)
    b.set(old_hash, Relation.VERSION_DATE, [_now_tag_date()])
    self.run_batch(b)
    self.sync(prev_col)

    self._detach_child(main_collection, old_hash, parent_uid)
    _trim_versions(self, prev_col, loc_key, self.config.prev_versions)


def _list_version_records(self: TieClient, prev_col: str, loc_key: str) -> list[dict]:
    try:
        rows, _ = self.query(QuerySpec(terms=[loc_key], filter=Relation.VERSION_OF,
                                       reverse=True, expand=True), collection=prev_col)
    except NotFound:
        return []
    return [{"hash": r.key, "filename": r.first(Relation.FILENAME),
             "tag_date": _parse_tag_date(r.first(Relation.VERSION_DATE))} for r in rows]


def _trim_versions(self: TieClient, prev_col: str, loc_key: str, keep: int) -> None:
    versions = self._list_version_records(prev_col, loc_key)
    for d in _retention_drops(versions, keep):
        _drop_version(self, prev_col, d["hash"], loc_key)


def _drop_version(self: TieClient, prev_col: str, hash: str, loc_key: str) -> None:
    edge = self.new_batch(prev_col)
    edge.delete(hash, Relation.VERSION_OF, loc_key)
    self.run_batch(edge)
    self.sync(prev_col)
    try:
        row = self.get(hash, collection=prev_col)
    except NotFound:
        return
    if row.values(Relation.VERSION_OF):
        return
    meta = self.new_batch(prev_col)
    for relation, values in row.attributes.items():
        for v in values:
            meta.delete(hash, relation, v)
    self.run_batch(meta)
    self.sync(prev_col)


def _resolve_file_loc(self: TieClient, path: str) -> tuple[str, str]:
    clean = path[len(FILE_URI_SCHEME):] if path.startswith(FILE_URI_SCHEME) else path
    if not clean.startswith("/"):
        clean = "/" + clean
    name = posixpath.basename(clean)
    dir = posixpath.dirname(clean)
    parent_uid = self.dir_uid_from_path(dir)
    if not parent_uid:
        raise FileNotFoundError(f"directory not found: {dir}")
    return parent_uid, name


def list_versions(self: TieClient, main_collection: str, path: str) -> list[VersionInfo]:
    parent_uid, name = self._resolve_file_loc(path)
    prev_col = self.config.prev_collection_for(main_collection)
    try:
        rows, _ = self.query(QuerySpec(terms=[_version_loc_key(parent_uid, name)],
                                       filter=Relation.VERSION_OF, reverse=True, expand=True),
                             collection=prev_col)
    except NotFound:
        return []
    from datetime import datetime
    out = [VersionInfo(hash=r.key, filename=r.first(Relation.FILENAME),
                       size=int(r.first(Relation.FILESIZE) or 0),
                       tie_type=r.first(Relation.TIE_TYPE),
                       date=_parse_tag_date(r.first(Relation.VERSION_DATE))) for r in rows]
    out.sort(key=lambda v: (-(v.date.timestamp() if v.date else 0), v.hash))
    return out


def restore_version(self: TieClient, main_collection: str, path: str, version_hash: str = "") -> str:
    parent_uid, name = self._resolve_file_loc(path)
    prev_col = self.config.prev_collection_for(main_collection)
    loc_key = _version_loc_key(parent_uid, name)
    versions = self.list_versions(main_collection, path)
    if not versions:
        raise ValueError(f"no versions recorded for {path}")
    target = versions[0].hash
    if version_hash:
        target = ""
        for v in versions:
            if v.hash == version_hash or v.hash.startswith(version_hash):
                target = v.hash
                break
        if not target:
            raise ValueError(f"no version {version_hash!r} for {path}")

    children = self._file_children(parent_uid)
    current = next((c["hash"] for c in children if c["filename"] == name), "")
    if current == target:
        return target
    if current:
        _supersede_to_prev(self, main_collection, parent_uid, name, current)

    prev_row = self.get(target, collection=prev_col)
    b = self.new_batch(main_collection)
    for relation, values in prev_row.attributes.items():
        if relation in (Relation.VERSION_OF, Relation.VERSION_DATE):
            continue
        for v in values:
            b.add(target, relation, v)
            if relation == Relation.TAG:
                b.add(Relation.TAGS, Relation.ALL, v)
    b.add(target, Relation.PARENT, parent_uid)
    self.run_batch(b)
    self.sync(main_collection)
    _drop_version(self, prev_col, target, loc_key)
    return target


TieClient._resolve_file_loc = _resolve_file_loc
TieClient._list_version_records = _list_version_records
TieClient.list_versions = list_versions
TieClient.restore_version = restore_version


# ---------------------------------------------------------------------------
# favorites
# ---------------------------------------------------------------------------


def list_favorites(self: TieClient) -> list[str]:
    try:
        row = self.get(Relation.TAGS)
    except NotFound:
        return []
    return sorted(row.values(Relation.FAVORITE))


def register_favorite(self: TieClient, tag: str) -> None:
    tag = tag.strip()
    if not tag:
        raise ValueError("RegisterFavorite: tag must not be empty")
    self.add(Relation.TAGS, Relation.FAVORITE, tag)
    self.sync()


def unregister_favorite(self: TieClient, tag: str) -> None:
    tag = tag.strip()
    if not tag:
        raise ValueError("UnregisterFavorite: tag must not be empty")
    self.delete(Relation.TAGS, Relation.FAVORITE, tag)
    self.sync()


TieClient.list_favorites = list_favorites
TieClient.register_favorite = register_favorite
TieClient.unregister_favorite = unregister_favorite


# ---------------------------------------------------------------------------
# co-tags (faceted refinement)
# ---------------------------------------------------------------------------


def cotags_for_query(self: TieClient, include: list[str], exclude: list[str], scope: str) -> list[str]:
    body = self._envelope("CoTags")
    body.update({"terms": include, "exclude": exclude or [], "scope": scope})
    reply = self._triplestore.run("CoTags", body)
    from .client import _check
    _check(reply)
    return reply.get("tags") or []


def cotags_for_query_excluding_input(self: TieClient, include: list[str], exclude: list[str],
                                     scope: str) -> list[str]:
    try:
        tags = self.cotags_for_query(include, exclude, scope)
    except NotFound:
        return []
    inputs = set(include)
    return [t for t in tags if t not in inputs]


TieClient.cotags_for_query = cotags_for_query
TieClient.cotags_for_query_excluding_input = cotags_for_query_excluding_input


# ---------------------------------------------------------------------------
# media-to-media relations
# ---------------------------------------------------------------------------


def relate_files(self: TieClient, from_hash: str, relation: str, to_hash: str) -> None:
    from .tiedir import is_hex_hash
    if not is_hex_hash(from_hash) or not is_hex_hash(to_hash):
        raise ValueError("RelateFiles: both endpoints must be content hashes")
    if not relation:
        raise ValueError("RelateFiles: relation must not be empty")
    b = self.new_batch()
    b.add(from_hash, relation, to_hash)
    b.add(to_hash, relation, from_hash)
    self.run_batch(b)


def relations_from(self: TieClient, hash: str) -> list[MediaRelation]:
    from .tiedir import is_hex_hash
    try:
        row = self.get(hash)
    except NotFound:
        return []
    rels = []
    for relation, values in row.attributes.items():
        for v in values:
            if is_hex_hash(v):
                rels.append(MediaRelation(from_hash=hash, relation=relation, to_hash=v))
    return rels


TieClient.relate_files = relate_files
TieClient.relations_from = relations_from


# ---------------------------------------------------------------------------
# Tables (mirrors client/table.go)
# ---------------------------------------------------------------------------
#
# A table entity T stores column and row order as ordinal-prefixed list values
# (the store returns a subject's multi-values sorted, so a zero-padded ordinal
# reproduces insertion order):
#
#   (T,  "tie-type", "table")
#   (T,  "columns",  ["<ord>\x00<header>", ...])
#   (T,  "rows",     ["<ord>\x00<row-uid>", ...])
#   (Ri, "tie-type", "table-row")
#   (Ri, <header>,   <cell>)          # one triple per non-empty cell
#
# A column named "tie-type" is unsupported (collides with the row type marker).
#
# A table may also carry a multi-row (hierarchical) header, where each column has
# an ordered list of header levels instead of a single label:
#
#   (T,  "column-levels", ["<ord>\x00<lvl0>\x1f<lvl1>", ...])
#
# Because a header keys every cell triple in its column, each column still needs
# exactly one unique string. A column's key is its non-empty levels joined by
# _LEVEL_SEP, and that is what "columns" holds; dropping empty levels means a
# single-row header keys as the label itself, so such tables are stored
# identically however they were written. Levels cannot be recovered from a key
# alone (empty levels are gone, so depth is not encoded), hence both relations.
# The relation is absent on single-level tables and read_table ignores it, so
# hierarchical headers are purely additive: older readers see the flat keys.

_TABLE_COLUMNS_REL = "columns"
_TABLE_ROWS_REL = "rows"
_TABLE_COLUMN_LEVELS_REL = "column-levels"
_ORDER_SEP = "\x00"

# Separates one column's header levels, both in a column-levels entry and in the
# derived column key. Unit Separator is used because it will not appear in a
# header label; _ORDER_SEP is unavailable, it already delimits the ordinal prefix.
_LEVEL_SEP = "\x1f"


def _encode_ordered(i: int, s: str) -> str:
    return f"{i:08d}{_ORDER_SEP}{s}"


def _decode_ordered_list(values: list[str]) -> list[str]:
    items = []
    for v in values:
        idx, sep, payload = v.partition(_ORDER_SEP)
        if not sep:
            continue
        try:
            n = int(idx)
        except ValueError:
            continue
        items.append((n, payload))
    items.sort(key=lambda t: t[0])
    return [p for _, p in items]


def _column_key(levels: list[str]) -> str:
    """Join a column's non-empty header levels into its single unique key."""
    return _LEVEL_SEP.join(l for l in levels if l != "")


def _transpose_header_rows(
    header_rows: list[list[str]],
) -> tuple[list[list[str]], list[str]]:
    """Convert row-major header rows into per-column levels plus derived keys.

    header_rows[i][j] is level i of column j, matching how a spreadsheet reads.
    """
    if not header_rows:
        return [], []
    width = len(header_rows[0])
    for i, hr in enumerate(header_rows):
        if len(hr) != width:
            raise ValueError(
                f"header row {i} has {len(hr)} columns, want {width}; "
                "header rows must be rectangular"
            )
    levels: list[list[str]] = []
    keys: list[str] = []
    for j in range(width):
        col = []
        for i, hr in enumerate(header_rows):
            if _LEVEL_SEP in hr[j] or _ORDER_SEP in hr[j]:
                raise ValueError(
                    f"header level {hr[j]!r} (row {i}, column {j}) contains a reserved separator"
                )
            col.append(hr[j])
        if _column_key(col) == "":
            raise ValueError(f"column {j} has no non-empty header level")
        levels.append(col)
        keys.append(_column_key(col))
    return levels, keys


def _transpose_levels(levels: list[list[str]]) -> list[list[str]]:
    """Convert per-column levels back into row-major header rows.

    Columns of unequal depth are padded with trailing empty levels so the result
    is rectangular.
    """
    depth = max((len(col) for col in levels), default=0)
    if depth == 0:
        return []
    return [
        [col[i] if i < len(col) else "" for col in levels]
        for i in range(depth)
    ]


def _decode_column_levels(values: list[str], keys: list[str]) -> list[list[str]]:
    """Return per-column header levels.

    A table stored without a column-levels relation — every table written before
    hierarchical headers existed, and every single-level table since — reads back
    as one level per column equal to its key, so both kinds share one read path.
    """
    entries = _decode_ordered_list(values)
    return [
        entries[j].split(_LEVEL_SEP) if j < len(entries) else [key]
        for j, key in enumerate(keys)
    ]


def _validate_column_keys(keys: list[str]) -> None:
    """Reject the two keys the storage layout cannot represent.

    A column key is a predicate, so duplicates would write both columns' cells to
    one triple, and "tie-type" would collide with the row entity's type marker.
    """
    if Relation.TIE_TYPE in keys:
        raise ValueError(
            f"column name {Relation.TIE_TYPE!r} is reserved and cannot be used as a table header"
        )
    if len(set(keys)) != len(keys):
        raise ValueError("table headers must be unique")


def _append_clear_table(self: TieClient, batch: Batch, uid: str, collection: str) -> None:
    try:
        row = self.get(uid, collection=collection)
    except NotFound:
        return
    old_row_uids = _decode_ordered_list(row.values(_TABLE_ROWS_REL))
    if not old_row_uids:
        return
    for r in self.expand(old_row_uids, collection=collection):
        for rel in list(r.attributes.keys()):
            batch.set(r.key, rel, [])


def insert_table(
    self: TieClient,
    uid: str,
    headers: list[str] | list[list[str]],
    rows: list[list[str]],
    collection: str = "",
) -> str:
    """Write headers + rows as a table entity and return its uid.

    An empty uid mints a fresh one; a non-empty uid replaces the table stored
    there (idempotent re-import), clearing its old row entities first. rows are
    row-major in header order; short rows are padded and extra cells ignored.
    Empty cells are not stored (read_table reconstructs them as "").

    headers is either a flat list of labels, or — for a multi-row (hierarchical)
    header — a list of header rows, row-major, so headers[i][j] is level i of
    column j. Header rows must be rectangular, with merged parent cells already
    forward-filled and blanks explicit: deciding where a merged cell ends is
    file-parsing work that belongs to the caller, not to storage. A single header
    row is stored identically to the equivalent flat call.
    """
    levels: list[list[str]] | None = None
    if headers and isinstance(headers[0], list):
        header_rows: list[list[str]] = headers  # type: ignore[assignment]
        levels, keys = _transpose_header_rows(header_rows)
        if len(header_rows) < 2:
            levels = None
    else:
        keys = headers  # type: ignore[assignment]
    _validate_column_keys(keys)

    minted = not uid
    if minted:
        uid = self.new_dir_uid()

    batch = self.new_batch(collection)
    if not minted:
        _append_clear_table(self, batch, uid, collection)

    columns = [_encode_ordered(j, k) for j, k in enumerate(keys)]
    batch.set(uid, Relation.TIE_TYPE, [TieType.TABLE])
    batch.set(uid, _TABLE_COLUMNS_REL, columns)

    # Cleared rather than skipped when there is no hierarchy, so a re-import that
    # drops one leaves nothing stale behind.
    batch.set(
        uid,
        _TABLE_COLUMN_LEVELS_REL,
        []
        if levels is None
        else [_encode_ordered(j, _LEVEL_SEP.join(col)) for j, col in enumerate(levels)],
    )

    row_refs = []
    for i, cells in enumerate(rows):
        row_uid = self.new_dir_uid()
        row_refs.append(_encode_ordered(i, row_uid))
        batch.set(row_uid, Relation.TIE_TYPE, [TieType.TABLE_ROW])
        for j, key in enumerate(keys):
            if j < len(cells) and cells[j] != "":
                batch.add(row_uid, key, cells[j])
    batch.set(uid, _TABLE_ROWS_REL, row_refs)

    self.run_batch(batch)
    return uid


def _read_table(
    self: TieClient, uid: str, collection: str
) -> tuple[list[str], list[list[str]], list[list[str]]]:
    """Shared reader: one get plus one expand serves both public readers.

    Returns (keys, per-column levels, rows), so asking for levels costs no extra
    round trip.
    """
    row = self.get(uid, collection=collection)
    keys = _decode_ordered_list(row.values(_TABLE_COLUMNS_REL))
    levels = _decode_column_levels(row.values(_TABLE_COLUMN_LEVELS_REL), keys)
    row_uids = _decode_ordered_list(row.values(_TABLE_ROWS_REL))
    if not row_uids:
        return keys, levels, []

    by_key = {r.key: r for r in self.expand(row_uids, collection=collection)}
    rows = []
    for ru in row_uids:
        r = by_key.get(ru)
        rows.append([r.first(k) if r else "" for k in keys])
    return keys, levels, rows


def read_table(self: TieClient, uid: str, collection: str = "") -> tuple[list[str], list[list[str]]]:
    """Return (headers, rows) for a table, row-major in header order.

    On a table with a multi-row header the headers are the derived column keys;
    use read_table_levels to get the levels. Raises NotFound if uid holds no
    table entity.
    """
    keys, _, rows = _read_table(self, uid, collection)
    return keys, rows


def read_table_levels(
    self: TieClient, uid: str, collection: str = ""
) -> tuple[list[list[str]], list[list[str]]]:
    """Return (header_rows, rows), header_rows in the shape insert_table accepts.

    A table stored with a single-row header yields exactly one header row, so
    callers need not distinguish the two cases. Raises NotFound if uid holds no
    table entity.
    """
    _, levels, rows = _read_table(self, uid, collection)
    return _transpose_levels(levels), rows


def delete_table(self: TieClient, uid: str, collection: str = "") -> None:
    """Remove a table and all its row entities.

    Idempotent: deleting a missing or already-deleted table is a no-op.
    """
    batch = self.new_batch(collection)
    _append_clear_table(self, batch, uid, collection)
    batch.set(uid, _TABLE_COLUMNS_REL, [])
    batch.set(uid, _TABLE_COLUMN_LEVELS_REL, [])
    batch.set(uid, _TABLE_ROWS_REL, [])
    batch.set(uid, Relation.TIE_TYPE, [])
    self.run_batch(batch)


TieClient.insert_table = insert_table
TieClient.read_table = read_table
TieClient.read_table_levels = read_table_levels
TieClient.delete_table = delete_table
