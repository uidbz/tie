"""Config.resolve_hosts: collection FileHosts override top-level defaults."""

import tomllib

from tie_client.config import CollectionEntry, Config, FileHost, _config_from_dict


def _cfg() -> Config:
    return Config(
        namespace="Collections",
        collection="Main",
        webservice="http://localhost:1",
        default_file_hosts=["top"],
        file_hosts={
            "top": FileHost(url="http://top"),
            "ssd": FileHost(url="http://ssd"),
            "hdd": FileHost(url="http://hdd"),
        },
        default_collection="Main",
        collections={
            "Main": CollectionEntry(namespace="Collections", collection="Main"),
            "media": CollectionEntry(collection="media", file_hosts=["ssd", "hdd"]),
        },
    )


def test_explicit_hosts_win():
    assert _cfg().resolve_hosts(["explicit"]) == ["explicit"]
    assert _cfg().resolve_hosts(["explicit"], "media") == ["explicit"]


def test_default_collection_inherits_top_level():
    assert _cfg().resolve_hosts([]) == ["top"]


def test_collection_file_hosts_override():
    # An explicit collection name (import --collection media) steers hosts.
    assert _cfg().resolve_hosts([], "media") == ["ssd", "hdd"]
    # So does the default collection when it has its own FileHosts.
    cfg = _cfg()
    cfg.default_collection = "media"
    assert cfg.resolve_hosts([]) == ["ssd", "hdd"]
    # resolve_host("") follows the same resolution.
    assert cfg.resolve_host("").url == "http://ssd"
    assert cfg.resolve_host("", "media").url == "http://ssd"


def test_collections_parse_from_toml():
    d = tomllib.loads(
        """
DefaultFileHosts = ['top']
DefaultCollection = 'media'

[FileHosts.top]
URL = 'http://top'

[FileHosts.ssd]
URL = 'http://ssd'

[Collections.media]
Collection = 'media'
FileHosts = ['ssd']
"""
    )
    cfg = _config_from_dict(d)
    assert cfg.default_collection == "media"
    assert cfg.collections["media"].file_hosts == ["ssd"]
    assert cfg.resolve_hosts([]) == ["ssd"]
