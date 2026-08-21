"""The tie triple vocabulary: relation names and tie-type values.

These strings are the wire vocabulary shared with the Go client
(client/tag.go). They must match exactly, so tags/types written by this
client interoperate with the Go CLI and vice-versa.
"""

from __future__ import annotations


class Relation:
    """Relation (value1) names, mirroring client.TieProperty stringers."""

    UID = "tie-uid"
    FILENAME = "filename"
    FILESIZE = "filesize"
    NAME = "name"
    MEDIA_TYPE = "media-type"
    FILEHOST = "filehost"
    TAG = "tag"
    PATH = "path"
    PARENT = "parent"
    TAGS = "tags"
    TAG_DATE = "tag-date"
    COLLECTION = "collection"
    TIE_TYPE = "tie-type"
    ALL = "all"
    TITLE = "title"
    ARTIST = "artist"
    ALBUM = "album"
    YEAR = "year"
    TRACK = "track"
    TIEDIR_HASH = "tiedir-hash"
    DIR_UID = "dir-uid"
    VERSION_OF = "version-of"
    VERSION_DATE = "version-date"
    FAVORITE = "favorite"


class TieType:
    """tie-type values, mirroring client.TieType stringers."""

    UNKNOWN_FILE = "unknown-file"
    IMAGE_FILE = "image-file"
    AUDIO_FILE = "audio-file"
    VIDEO_FILE = "video-file"
    DOCUMENT_FILE = "document-file"
    ARCHIVE_FILE = "archive-file"
    IMAGE_DIR = "image-dir"
    AUDIO_DIR = "audio-dir"
    VIDEO_DIR = "video-dir"
    DOCUMENT_DIR = "document-dir"
    IMAGE_ARCHIVE = "image-archive"
    AUDIO_ARCHIVE = "audio-archive"
    VIDEO_ARCHIVE = "video-archive"
    DOCUMENT_ARCHIVE = "document-archive"
    DIRECTORY = "directory"
    FILE = "file"
    TABLE = "table"
    TABLE_ROW = "table-row"


# Archive tie-types, most specific first (mirrors client.archiveTypes).
ARCHIVE_TYPES = [
    TieType.IMAGE_ARCHIVE,
    TieType.AUDIO_ARCHIVE,
    TieType.VIDEO_ARCHIVE,
    TieType.DOCUMENT_ARCHIVE,
    TieType.ARCHIVE_FILE,
]

# Directory tie-types, for ListDirTypes' built-in union (TieImageDir..TieDirectory).
DIR_TYPES = [
    TieType.IMAGE_DIR,
    TieType.AUDIO_DIR,
    TieType.VIDEO_DIR,
    TieType.DOCUMENT_DIR,
    TieType.IMAGE_ARCHIVE,
    TieType.AUDIO_ARCHIVE,
    TieType.VIDEO_ARCHIVE,
    TieType.DOCUMENT_ARCHIVE,
    TieType.DIRECTORY,
]

# The private URI scheme prefixing every (uid, "path", ...) triple.
FILE_URI_SCHEME = "tie:"

# Single-valued tag-date layout. Go: "2006-01-02 15:04:05.000000".
TAG_DATE_FORMAT = "%Y-%m-%d %H:%M:%S.%f"


def is_archive_type(t: str) -> bool:
    return t in ARCHIVE_TYPES


def archive_type_of(types: list[str]) -> str:
    """Return the most specific archive type present, else archive-file."""
    for t in ARCHIVE_TYPES:
        if t in types:
            return t
    return TieType.ARCHIVE_FILE


def contains_archive_type(types: list[str]) -> bool:
    return any(t in types for t in ARCHIVE_TYPES)
