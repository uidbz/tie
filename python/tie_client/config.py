"""Client configuration: the Config/FileHost model and TOML load/save.

Mirrors client.Config (client/tie.go) and the conf package's search/save
behavior. Reading uses stdlib tomllib; writing is a small hand-rolled emitter
(stdlib has no TOML writer) covering exactly the keys we persist.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class FileHost:
    url: str = ""
    insecure: bool = False
    username: str = ""
    password: str = ""


@dataclass
class CollectionEntry:
    """One named [Collections.<name>] binding; unset fields fall back to the
    top-level Config values. Only file_hosts steers host resolution here."""

    namespace: str = ""
    collection: str = ""
    triple_store_url: str = ""
    username: str = ""
    password: str = ""
    insecure: bool = False
    file_hosts: list[str] = field(default_factory=list)


@dataclass
class Config:
    username: str = ""
    password: str = ""
    namespace: str = ""
    collection: str = ""
    webservice: str = ""
    webservice_insecure: bool = False
    default_file_hosts: list[str] = field(default_factory=list)
    file_hosts: dict[str, FileHost] = field(default_factory=dict)
    default_collection: str = ""
    collections: dict[str, CollectionEntry] = field(default_factory=dict)
    import_dest: dict[str, str] = field(default_factory=dict)
    # NOTE: like the Go client, a TOML file that omits PrevVersions loads 0
    # (no history), NOT the default_config() value of 3.
    prev_versions: int = 0
    queries: dict[str, str] = field(default_factory=dict)
    config_path: str = ""

    def resolve_host(self, name: str, collection: str = "") -> FileHost:
        """Look up a filehost by name; empty name selects the first default."""
        if not name:
            hosts = self.resolve_hosts([], collection)
            if not hosts:
                raise ValueError(
                    "no filehost configured: set DefaultFileHosts or pass a host name"
                )
            name = hosts[0]
        host = self.file_hosts.get(name)
        if host is None:
            raise ValueError(f"unknown filehost '{name}'")
        return host

    def resolve_hosts(self, explicit: list[str], collection: str = "") -> list[str]:
        """Pick the filehost names an upload/import targets.

        An explicit --host list wins; otherwise the named collection's
        FileHosts are used (the default collection when collection is empty)
        — its own list when set, the top-level default_file_hosts when not —
        mirroring the Go client's TieClient.ResolveHosts.
        """
        if explicit:
            return list(explicit)
        entry = self.collections.get(collection or self.default_collection)
        if entry is not None and entry.file_hosts:
            return list(entry.file_hosts)
        return list(self.default_file_hosts)

    def prev_collection_for(self, main_collection: str) -> str:
        if not main_collection:
            main_collection = self.collection
        return main_collection + "_prev"


def default_config() -> Config:
    """The built-in localhost default (mirrors client.defaultConfig)."""
    return Config(
        username="defaultuser",
        password="defaultpassword",
        namespace="Collections",
        collection="Main",
        webservice="http://localhost:1161",
        default_file_hosts=["default"],
        file_hosts={"default": FileHost(url="http://localhost:1162")},
        prev_versions=3,
    )


def _config_file_name(name: str) -> str:
    if name and not name.endswith(".toml"):
        return name + ".toml"
    return name


def _user_config_path(name: str) -> Path:
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config")
    return Path(base) / "tie" / name


def _search_paths(name: str) -> list[Path]:
    # If the name looks like a path that exists, use it directly.
    direct = Path(name)
    paths = [direct, Path.cwd() / name, _user_config_path(name)]
    return paths


def _config_from_dict(d: dict) -> Config:
    fh: dict[str, FileHost] = {}
    for host_name, hv in (d.get("FileHosts") or {}).items():
        fh[host_name] = FileHost(
            url=hv.get("URL", ""),
            insecure=hv.get("Insecure", False),
            username=hv.get("Username", ""),
            password=hv.get("Password", ""),
        )
    cols: dict[str, CollectionEntry] = {}
    for coll_name, cv in (d.get("Collections") or {}).items():
        cols[coll_name] = CollectionEntry(
            namespace=cv.get("Namespace", ""),
            collection=cv.get("Collection", ""),
            triple_store_url=cv.get("TripleStoreURL", ""),
            username=cv.get("Username", ""),
            password=cv.get("Password", ""),
            insecure=cv.get("Insecure", False),
            file_hosts=list(cv.get("FileHosts", []) or []),
        )
    return Config(
        username=d.get("Username", ""),
        password=d.get("Password", ""),
        namespace=d.get("Namespace", ""),
        collection=d.get("Collection", ""),
        webservice=d.get("Webservice", ""),
        webservice_insecure=d.get("WebserviceInsecure", False),
        default_file_hosts=list(d.get("DefaultFileHosts", []) or []),
        file_hosts=fh,
        default_collection=d.get("DefaultCollection", ""),
        collections=cols,
        import_dest=dict(d.get("ImportDest", {}) or {}),
        prev_versions=int(d.get("PrevVersions", 0) or 0),
        queries=dict(d.get("Queries", {}) or {}),
    )


def load_config(name: str) -> Config:
    """Load a config by name (searching CWD and the user config dir).

    Raises FileNotFoundError when no config exists in any searched location.
    """
    name = _config_file_name(name) or "config.toml"
    for path in _search_paths(name):
        if path.is_file():
            with open(path, "rb") as f:
                data = tomllib.load(f)
            cfg = _config_from_dict(data)
            cfg.config_path = str(path)
            return cfg
    raise FileNotFoundError(f"no tie config named {name!r} found")


def _toml_str(s: str) -> str:
    # Prefer a literal single-quoted string (no escaping); fall back to a basic
    # double-quoted string when the value contains a quote/backslash/control char.
    if "'" not in s and "\\" not in s and all(ord(c) >= 0x20 for c in s):
        return f"'{s}'"
    escaped = s.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n").replace("\t", "\\t")
    return f'"{escaped}"'


def save_config(name: str, config: Config) -> str:
    """Write config to the user config dir; return the written path."""
    name = _config_file_name(name) or "config.toml"
    path = _user_config_path(name)
    path.parent.mkdir(parents=True, exist_ok=True)

    lines: list[str] = []
    lines.append(f"Username = {_toml_str(config.username)}")
    lines.append(f"Password = {_toml_str(config.password)}")
    lines.append(f"Namespace = {_toml_str(config.namespace)}")
    lines.append(f"Collection = {_toml_str(config.collection)}")
    lines.append(f"Webservice = {_toml_str(config.webservice)}")
    if config.webservice_insecure:
        lines.append("WebserviceInsecure = true")
    hosts = ", ".join(_toml_str(h) for h in config.default_file_hosts)
    lines.append(f"DefaultFileHosts = [{hosts}]")
    if config.default_collection:
        lines.append(f"DefaultCollection = {_toml_str(config.default_collection)}")
    lines.append(f"PrevVersions = {config.prev_versions}")
    lines.append("")
    for host_name, h in config.file_hosts.items():
        lines.append(f"[FileHosts.{host_name}]")
        lines.append(f"URL = {_toml_str(h.url)}")
        lines.append(f"Insecure = {'true' if h.insecure else 'false'}")
        if h.username:
            lines.append(f"Username = {_toml_str(h.username)}")
        if h.password:
            lines.append(f"Password = {_toml_str(h.password)}")
        lines.append("")
    for coll_name, e in config.collections.items():
        lines.append(f"[Collections.{coll_name}]")
        if e.namespace:
            lines.append(f"Namespace = {_toml_str(e.namespace)}")
        if e.collection:
            lines.append(f"Collection = {_toml_str(e.collection)}")
        if e.triple_store_url:
            lines.append(f"TripleStoreURL = {_toml_str(e.triple_store_url)}")
        if e.username:
            lines.append(f"Username = {_toml_str(e.username)}")
        if e.password:
            lines.append(f"Password = {_toml_str(e.password)}")
        if e.insecure:
            lines.append("Insecure = true")
        if e.file_hosts:
            hosts = ", ".join(_toml_str(h) for h in e.file_hosts)
            lines.append(f"FileHosts = [{hosts}]")
        lines.append("")
    if config.import_dest:
        lines.append("[ImportDest]")
        for k, v in config.import_dest.items():
            lines.append(f"{k} = {_toml_str(v)}")
        lines.append("")
    if config.queries:
        lines.append("[Queries]")
        for k, v in config.queries.items():
            lines.append(f"{k} = {_toml_str(v)}")
        lines.append("")

    path.write_text("\n".join(lines))
    return str(path)


def user_config_dir_path(name: str) -> str:
    return str(_user_config_path(_config_file_name(name) or "config.toml"))
