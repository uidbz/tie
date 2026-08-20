"""The `tie` command-line interface.

Mirrors cmd/tie/commands.go (non-FUSE subcommands): add, get, del, upload,
download, import (+ per-dir-type/archive subcommands), tag, versions, dump,
restore, conf create.
"""

from __future__ import annotations

import argparse
import csv
import sys

from . import vocab
from .client import QuerySpec, TieClient
from .config import (
    Config,
    FileHost,
    default_config,
    load_config,
    save_config,
    user_config_dir_path,
)
from .errors import NotFound
from .vocab import TieType, is_archive_type

BUILTIN_DIR_TYPES = ["audio-dir", "image-dir", "video-dir", "document-dir"]
BUILTIN_ARCHIVE_TYPES = ["image-archive", "audio-archive", "video-archive", "document-archive"]


def _load_client(config_name: str) -> TieClient | None:
    try:
        cfg = load_config(config_name)
    except FileNotFoundError:
        return None
    return TieClient(cfg)


def _resolve_file_host(tie: TieClient | None, args) -> FileHost:
    if getattr(args, "server", None):
        return FileHost(url=args.server, insecure=getattr(args, "insecure", False))
    if tie is None:
        raise SystemExit("Error: Config not loaded")
    return tie.resolve_host(getattr(args, "host", "") or "")


def _print_rows(rows) -> None:
    for r in rows:
        for relation, values in r.attributes.items():
            for v in values:
                print(f"{r.key}\t{relation}\t{v}")


# --- command handlers ---


def _cmd_add(tie, args):
    tie.add(args.key, args.value1, args.value2)


def _cmd_get(tie, args):
    extra = args.rest or []
    reverse = True if extra else args.reverse
    terms = [args.key]
    exclude = []
    for a in extra:
        if a.startswith("-"):
            exclude.append(a[1:])
        else:
            terms.append(a)
    try:
        rows, _ = tie.query(QuerySpec(terms=terms, exclude=exclude, reverse=reverse,
                                      filter=args.filter or "", offset=args.offset,
                                      limit=args.limit, sort_by=args.sortby or ""))
    except NotFound:
        return
    _print_rows(rows)


def _cmd_del(tie, args):
    key, v1, v2 = args.key, args.value1, args.value2

    def delete_batch(filter_):
        try:
            rows, _ = tie.query(QuerySpec(terms=[key], filter=filter_, limit=-1))
        except NotFound:
            return
        b = tie.new_batch()
        for r in rows:
            for relation, values in r.attributes.items():
                for v in values:
                    b.delete(r.key, relation, v)
        tie.run_batch(b)

    if v1 == "*" and v2 == "*":
        delete_batch("")
    elif v1 != "*" and v2 == "*":
        delete_batch(v1)
    elif v1 == "*" and v2 != "*":
        print("Function to delete specific value2 from all value1's is not implemented, "
              "because it is most likely a typo.")
        print(f"Did you mean: tie delete {key} {v2} * ?")
    else:
        tie.delete(key, v1, v2)


def _cmd_upload(tie, args):
    host = _resolve_file_host(tie, args)
    result = tie.upload_to(host, args.path)
    if result.error_msg and not result.items:
        raise SystemExit(result.error_msg)
    if args.json:
        import json
        print(json.dumps({"Items": [vars(i) | {"head": ""} for i in result.items],
                          "ErrorMsg": result.error_msg}, default=str))
    else:
        for item in result.items:
            print(f"{item.hash}\t{item.filename}")


def _cmd_download(tie, args):
    host = _resolve_file_host(tie, args)
    tie.download_from(host, args.source_hash, args.dest)


def _cmd_dump(tie, args):
    w = csv.writer(sys.stdout, delimiter="\t", lineterminator="\n")
    for k, v1, v2 in tie.dump():
        w.writerow([k, v1, v2])


def _cmd_restore(tie, args):
    src = open(args.file, newline="") if args.file else sys.stdin
    try:
        triples = []
        reader = csv.reader(src, delimiter="\t")
        for line_no, rec in enumerate(reader, 1):
            if len(rec) != 3:
                raise SystemExit(f"Malformed line {line_no}: expected 3 fields, got {len(rec)}")
            triples.append((rec[0], rec[1], rec[2]))
    finally:
        if args.file:
            src.close()
    if args.drop:
        tie.drop_collection()
    tie.restore(triples)
    tie.sync()
    print(f"Restored {len(triples)} triples", file=sys.stderr)


def _run_import(tie, args, label):
    import os
    dir_type = label
    forced_archive = ""
    if is_archive_type(label):
        forced_archive = label
        dir_type = TieType.DIRECTORY
    hosts = args.host or tie.config.default_file_hosts
    for path in args.paths:
        if not os.path.exists(path):
            print(f"{path}: not found", file=sys.stderr)
            continue
        print("Uploading to", hosts)
        for h in hosts:
            fh = tie.config.file_hosts.get(h)
            if fh is None:
                print(f"unknown filehost '{h}'", file=sys.stderr)
                continue
            try:
                if os.path.isdir(path):
                    tie.import_dir(path, fh, args.collection or "", dir_type,
                                   args.tags or [], args.dest or "", forced_archive)
                else:
                    tie.import_file(path, fh, args.collection or "", args.tags or [],
                                    "", forced_archive)
            except Exception as e:  # noqa: BLE001 - match Go's print-and-continue
                print(e, file=sys.stderr)


# --- tag subcommands ---


def _cmd_tag_list(tie, args):
    tags, _ = tie.list_tags(args.offset, args.limit)
    for t in tags:
        print(t)


def _cmd_tag_add(tie, args):
    tie.register_tag(args.tag)


def _cmd_tag_del(tie, args):
    n = tie.delete_tag(args.tag)
    print(f"Removed tag from {n} item(s)", file=sys.stderr)


def _cmd_tag_rename(tie, args):
    n = tie.rename_tag(args.tag, args.newname)
    print(f"Renamed tag on {n} item(s)", file=sys.stderr)


def _cmd_tag_files(tie, args):
    files, _ = tie.files_with_tags("", [args.tag], None, 0, -1)
    for f in files:
        print(f"{f.hash}\t{f.filename}")


def _cmd_tag_untagged(tie, args):
    files, _ = tie.untagged_files(args.type or "", args.offset, args.limit)
    for f in files:
        print(f"{f.hash}\t{f.filename}")


def _cmd_tag_show(tie, args):
    for t in tie.get_tags(args.hash):
        print(t)


def _cmd_tag_set(tie, args):
    tie.set_tags(args.hash, args.tags or [])


def _cmd_versions_list(tie, args):
    for v in tie.list_versions("", args.path):
        date = v.date.isoformat() if v.date else ""
        print(f"{v.hash}\t{date}\t{v.size}\t{v.filename}")


def _cmd_versions_restore(tie, args):
    h = tie.restore_version("", args.path, args.hash or "")
    print(h)


def _cmd_conf_create(args):
    name = args.name or "config"
    save_config(name, default_config())
    print("Config created here:", user_config_dir_path(name))


# --- parser ---


def build_parser(config: Config | None) -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="tie", description="tie client")
    p.add_argument("-c", "--config", default="config", help="config name (default: config)")
    sub = p.add_subparsers(dest="command", required=True)

    a = sub.add_parser("add", aliases=["a"], help="Add a triple: add key value1 value2")
    a.add_argument("key"); a.add_argument("value1"); a.add_argument("value2")
    a.set_defaults(func=_cmd_add, needs_tie=True)

    g = sub.add_parser("get", aliases=["g"], help="Get triples: get key [more...]")
    g.add_argument("key"); g.add_argument("rest", nargs="*")
    g.add_argument("-r", "--reverse", action="store_true")
    g.add_argument("-l", "--limit", type=int, default=1000)
    g.add_argument("-o", "--offset", type=int, default=0)
    g.add_argument("-s", "--sortby", default="")
    g.add_argument("-f", "--filter", default="")
    g.set_defaults(func=_cmd_get, needs_tie=True)

    d = sub.add_parser("del", aliases=["d"], help="Delete a triple: del key value1 value2")
    d.add_argument("key"); d.add_argument("value1"); d.add_argument("value2")
    d.set_defaults(func=_cmd_del, needs_tie=True)

    u = sub.add_parser("upload", help="Upload a file or directory: upload path")
    u.add_argument("path")
    u.add_argument("--host", default=""); u.add_argument("--server", default="")
    u.add_argument("--insecure", action="store_true"); u.add_argument("--json", action="store_true")
    u.set_defaults(func=_cmd_upload, needs_tie=True)

    dl = sub.add_parser("download", help="Download: download source-hash dest")
    dl.add_argument("source_hash"); dl.add_argument("dest")
    dl.add_argument("--host", default=""); dl.add_argument("--server", default="")
    dl.add_argument("--insecure", action="store_true")
    dl.set_defaults(func=_cmd_download, needs_tie=True)

    dp = sub.add_parser("dump", help="Dump every triple as TSV")
    dp.set_defaults(func=_cmd_dump, needs_tie=True)

    rs = sub.add_parser("restore", help="Restore triples from TSV (stdin if no file)")
    rs.add_argument("file", nargs="?", default="")
    rs.add_argument("--drop", action="store_true")
    rs.set_defaults(func=_cmd_restore, needs_tie=True)

    _add_import(sub, config)
    _add_tag(sub)
    _add_versions(sub)

    conf = sub.add_parser("conf", aliases=["c"], help="Config management")
    conf_sub = conf.add_subparsers(dest="conf_command", required=True)
    cc = conf_sub.add_parser("create", help="Create a default config: conf create [name]")
    cc.add_argument("name", nargs="?", default="")
    cc.set_defaults(func=lambda tie, args: _cmd_conf_create(args), needs_tie=False)

    return p


def _import_flags(sp):
    sp.add_argument("paths", nargs="+")
    sp.add_argument("--collection", default="")
    sp.add_argument("-t", "--tags", action="append", default=[])
    sp.add_argument("--host", action="append", default=[])
    sp.add_argument("--dest", default="")


def _import_dir_types(config: Config | None) -> list[str]:
    types = list(BUILTIN_DIR_TYPES) + list(BUILTIN_ARCHIVE_TYPES)
    seen = set(types)
    if config:
        for name in config.import_dest:
            if name not in seen:
                seen.add(name)
                types.append(name)
    return types


def _add_import(sub, config: Config | None):
    imp = sub.add_parser("import", aliases=["i"], help="Import files and tag them")
    _import_flags(imp)
    imp.set_defaults(func=lambda tie, args: _run_import(tie, args, TieType.DIRECTORY), needs_tie=True)
    imp_sub = imp.add_subparsers(dest="import_type")
    for name in _import_dir_types(config):
        dsp = imp_sub.add_parser(name, help=f"import, labeling roots as {name}")
        _import_flags(dsp)
        dsp.set_defaults(func=(lambda n: lambda tie, args: _run_import(tie, args, n))(name),
                         needs_tie=True)


def _add_tag(sub):
    tag = sub.add_parser("tag", help="Manage tags")
    ts = tag.add_subparsers(dest="tag_command", required=True)
    tl = ts.add_parser("list"); tl.add_argument("-o", "--offset", type=int, default=0)
    tl.add_argument("-l", "--limit", type=int, default=0)
    tl.set_defaults(func=_cmd_tag_list, needs_tie=True)
    ta = ts.add_parser("add"); ta.add_argument("tag"); ta.set_defaults(func=_cmd_tag_add, needs_tie=True)
    td = ts.add_parser("del"); td.add_argument("tag"); td.set_defaults(func=_cmd_tag_del, needs_tie=True)
    tr = ts.add_parser("rename"); tr.add_argument("tag"); tr.add_argument("newname")
    tr.set_defaults(func=_cmd_tag_rename, needs_tie=True)
    tf = ts.add_parser("files"); tf.add_argument("tag"); tf.set_defaults(func=_cmd_tag_files, needs_tie=True)
    tu = ts.add_parser("untagged"); tu.add_argument("-t", "--type", default="")
    tu.add_argument("-o", "--offset", type=int, default=0); tu.add_argument("-l", "--limit", type=int, default=0)
    tu.set_defaults(func=_cmd_tag_untagged, needs_tie=True)
    tsh = ts.add_parser("show"); tsh.add_argument("hash"); tsh.set_defaults(func=_cmd_tag_show, needs_tie=True)
    tst = ts.add_parser("set"); tst.add_argument("hash"); tst.add_argument("tags", nargs="*")
    tst.set_defaults(func=_cmd_tag_set, needs_tie=True)


def _add_versions(sub):
    v = sub.add_parser("versions", help="Inspect and restore file version history")
    vs = v.add_subparsers(dest="versions_command", required=True)
    vl = vs.add_parser("list"); vl.add_argument("path"); vl.set_defaults(func=_cmd_versions_list, needs_tie=True)
    vr = vs.add_parser("restore"); vr.add_argument("path"); vr.add_argument("hash", nargs="?", default="")
    vr.set_defaults(func=_cmd_versions_restore, needs_tie=True)


def main(argv: list[str] | None = None) -> int:
    argv = argv if argv is not None else sys.argv[1:]
    # Peek the config name so import subcommands can reflect [ImportDest] keys.
    pre = argparse.ArgumentParser(add_help=False)
    pre.add_argument("-c", "--config", default="config")
    known, _ = pre.parse_known_args(argv)
    try:
        config = load_config(known.config)
    except FileNotFoundError:
        config = None

    parser = build_parser(config)
    args = parser.parse_args(argv)

    tie = None
    if getattr(args, "needs_tie", False):
        tie = _load_client(args.config)
        if tie is None:
            print("Error: Config not loaded", file=sys.stderr)
            return 1
    try:
        args.func(tie, args)
    except SystemExit:
        raise
    except Exception as e:  # noqa: BLE001
        print(f"Error: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
