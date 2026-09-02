"""End-to-end tests against a running test-env (triplestore :2161, filehost :2162).

Skipped automatically when the servers are not reachable. Run test-env first:
    cd test-env && ./build.sh && ./start.sh
"""

import os
import shutil
import subprocess
import tempfile
import urllib.request

import tie_client as t
from tie_client import Config, FileHost, QuerySpec

WEBSERVICE = "http://localhost:2161"
FILEHOST = "http://localhost:2162"


def servers_up() -> bool:
    try:
        urllib.request.urlopen(WEBSERVICE, timeout=1)
    except urllib.error.HTTPError:
        return True  # reachable, just no route for GET /
    except Exception:
        return False
    return True


def make_client(collection="Main") -> t.TieClient:
    cfg = Config(
        username="defaultuser", password="defaultpassword",
        namespace="Collections", collection=collection,
        webservice=WEBSERVICE, default_file_hosts=["default"],
        file_hosts={"default": FileHost(url=FILEHOST)}, prev_versions=3,
    )
    return t.TieClient(cfg)


def go_tie(*args) -> str:
    """Run the Go tie CLI from test-env against the same servers."""
    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    env_dir = os.path.join(root, "test-env")
    cmd = [os.path.join(env_dir, "bin", "tie"), "-C", "config.toml", *args]
    out = subprocess.run(cmd, cwd=env_dir, capture_output=True, text=True)
    return out.stdout


def test_triple_round_trip():
    tie = make_client()
    tie.add("py:e2e:key", "py:e2e:rel", "py:e2e:val")
    tie.sync()
    row = tie.get("py:e2e:key")
    assert row.first("py:e2e:rel") == "py:e2e:val"
    tie.delete("py:e2e:key", "py:e2e:rel", "py:e2e:val")
    tie.sync()
    # After deleting the only triple, the subject lingers with empty attributes
    # (matching the Go triplestore's Expand), so the relation/value must be gone but
    # the call need not raise NotFound.
    try:
        row = tie.get("py:e2e:key")
        assert "py:e2e:rel" not in row.attributes, row.attributes
    except t.NotFound:
        pass


def test_file_round_trip():
    tie = make_client()
    host = tie.resolve_host("")
    tmp = tempfile.mkdtemp()
    try:
        src = os.path.join(tmp, "a.txt")
        with open(src, "w") as f:
            f.write("hello python client")
        res = tie.upload_to(host, src)
        h = res.items[-1].hash
        dest = os.path.join(tmp, "out.txt")
        tie.download_from(host, h, dest)
        assert open(dest).read() == "hello python client"
    finally:
        shutil.rmtree(tmp)


def _build_tree(root):
    os.makedirs(os.path.join(root, "sub"))
    with open(os.path.join(root, "a.txt"), "w") as f:
        f.write("hello python client")
    with open(os.path.join(root, "c.bin"), "w") as f:
        f.write("PNGDATA")
    with open(os.path.join(root, "sub", "b.txt"), "w") as f:
        f.write("nested file body")


def test_dir_upload_download_and_hash_parity():
    """The Python dir hash must equal the Go CLI's for the same tree."""
    tie = make_client()
    host = tie.resolve_host("")
    tmp = tempfile.mkdtemp()
    try:
        tree = os.path.join(tmp, "tree")
        _build_tree(tree)

        res = tie.upload_to(host, tree)
        py_hash = res.items[-1].hash

        # Go CLI upload of the identical tree prints "hash\tfilename"; the root
        # (last line) is the directory hash.
        go_out = go_tie("upload", tree)
        go_hash = go_out.strip().splitlines()[-1].split("\t")[0]
        assert py_hash == go_hash, f"py={py_hash} go={go_hash}"

        # Download+expand round-trips the whole tree.
        out = os.path.join(tmp, "out")
        tie.download_from(host, py_hash, out)
        got = {}
        for r, _, files in os.walk(out):
            for fn in files:
                p = os.path.join(r, fn)
                got[os.path.relpath(p, out)] = open(p).read()
        assert got == {
            "a.txt": "hello python client",
            "c.bin": "PNGDATA",
            os.path.join("sub", "b.txt"): "nested file body",
        }, got
    finally:
        shutil.rmtree(tmp)


def test_import_and_tags():
    tie = make_client()
    host = tie.resolve_host("")
    tmp = tempfile.mkdtemp()
    try:
        tree = os.path.join(tmp, "album")
        _build_tree(tree)
        tie.import_dir(tree, host, "", "audio-dir", tags=["e2etag"], dest="/py-e2e-album")
        files, _ = tie.files_with_tags("", ["e2etag"], None)
        names = sorted(f.filename for f in files)
        # Every file and directory in the tree carries the tag; the root dir is
        # named from --dest ("py-e2e-album").
        assert "a.txt" in names and "py-e2e-album" in names, names

        # Cross-client: Go CLI sees the same tagged files.
        go_out = go_tie("tag", "files", "e2etag")
        assert "a.txt" in go_out, go_out
    finally:
        shutil.rmtree(tmp)


def test_dump_restore_round_trip():
    tie = make_client(collection="PyDumpTest")
    tie.add("dr:k1", "dr:r", "v1")
    tie.add("dr:k2", "dr:r", "v2")
    tie.sync()
    dumped = list(tie.dump())
    assert ("dr:k1", "dr:r", "v1") in dumped
    tie.drop_collection()
    tie.sync()
    tie.restore(dumped)
    tie.sync()
    row = tie.get("dr:k2")
    assert row.first("dr:r") == "v2"


def run_all():
    if not servers_up():
        print("SKIP: test-env servers not reachable on :2161/:2162")
        return 0
    import traceback
    failed = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS {name}")
            except Exception:
                failed += 1
                print(f"FAIL {name}")
                traceback.print_exc()
    print("FAILED" if failed else "ALL E2E PASS")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(run_all())
