"""Tables against a running test-env triplestore (:2161).

A table is a client-side convenience over triples, so its encoding is
hand-mirrored between client/table.go and tie_client.highlevel with nothing but
these tests holding the two in step. Consumers are promised the clients
interoperate, so the round-trip cases run in Python while the cross-client cases
write with one client and read with the other via gotable.go.

Skipped automatically when the triplestore is not reachable. Run test-env first:
    cd test-env && ./build.sh && ./start.sh
"""

import json
import os
import socket
import subprocess

import pytest

import tie_client as t
from tie_client import Config

# The stored layout is what cross-client compatibility rests on, so a few tests
# assert on the raw relations rather than only on what read_table returns.
from tie_client.highlevel import (
    _TABLE_COLUMN_LEVELS_REL,
    _TABLE_COLUMNS_REL,
    _TABLE_ROWS_REL,
    _decode_ordered_list,
    _transpose_header_rows,
    _transpose_levels,
    _validate_column_keys,
)

TRIPLESTORE = "http://localhost:2161"
ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def require_server() -> None:
    """Skip unless the test-env triplestore answers, mirroring Go's requireServer."""
    try:
        socket.create_connection(("localhost", 2161), timeout=1).close()
    except OSError as err:
        pytest.skip(f"no triplestore at {TRIPLESTORE}: {err}")


def make_client() -> t.TieClient:
    """A client on the namespace/collection Go's client.TestingConfig() uses.

    Cross-client tests only see each other's tables if both clients resolve to the
    same collection, and the Go fixture builds its client from TestingConfig — so
    this mirrors that rather than test_e2e's Collections/Main.
    """
    cfg = Config(
        username="defaultuser", password="defaultpassword",
        namespace="testing", collection="testing",
        webservice=TRIPLESTORE, prev_versions=3,
    )
    return t.TieClient(cfg)


def go_table(mode: str, uid: str, payload: dict | None = None) -> str:
    """Run the Go table fixture (gotable.go) against the same triplestore."""
    cmd = ["go", "run", os.path.join("python", "tests", "gotable.go"), mode, uid]
    out = subprocess.run(cmd, cwd=ROOT, input=json.dumps(payload or {}),
                         capture_output=True, text=True)
    if out.returncode != 0:
        raise AssertionError(f"gotable {mode} {uid} failed: {out.stderr}")
    return out.stdout.strip()


def go_write(uid: str, headers: list, rows: list[list[str]]) -> str:
    return go_table("write", uid, {"headers": headers, "rows": rows})


def go_read(uid: str) -> tuple[list[list[str]], list[list[str]]]:
    got = json.loads(go_table("read", uid))
    return got["headers"], got["rows"]


# The two-row header used by most of the levels tests: a plain index column beside
# two temperature groups, each holding two replicates. The parent labels repeat —
# a merged parent cell is forward-filled by the caller, not by the client.
LEVELS_HEADER_ROWS = [
    ["Sample", "Temperature (20°C)", "Temperature (20°C)", "Temperature (37°C)", "Temperature (37°C)"],
    ["", "Replicate 1", "Replicate 2", "Replicate 1", "Replicate 2"],
]
LEVELS_ROWS = [
    ["S1", "4.2", "4.4", "5.9", "6.1"],
    ["S2", "4.1", "", "6.0", "6.3"],  # empty cell
]
LEVELS_WANT_KEYS = [
    "Sample",
    "Temperature (20°C)\x1fReplicate 1",
    "Temperature (20°C)\x1fReplicate 2",
    "Temperature (37°C)\x1fReplicate 1",
    "Temperature (37°C)\x1fReplicate 2",
]


def test_header_level_transpose():
    """The pure header helpers need no server: row-major in, per-column out, and back."""
    levels, keys = _transpose_header_rows(LEVELS_HEADER_ROWS)
    assert keys == LEVELS_WANT_KEYS
    # An index column's blank lower level is dropped from its key but kept in its
    # levels, so the column still reports its true depth.
    assert levels[0] == ["Sample", ""]
    assert _transpose_levels(levels) == LEVELS_HEADER_ROWS


@pytest.mark.parametrize("header_rows", [
    pytest.param([["A", "B"], ["x"]], id="not rectangular"),
    pytest.param([["A", "B\x1fC"]], id="reserved separator"),
    pytest.param([["A", ""], ["B", ""]], id="column with no label"),
    pytest.param([["Model", "Model"], ["p", "p"]], id="levels deriving a duplicate key"),
])
def test_header_levels_rejected(header_rows):
    """The header shapes the storage layout cannot represent.

    Rejection lands in the transpose or in key validation depending on the case, so
    both run here exactly as insert_table chains them.
    """
    with pytest.raises(ValueError):
        _, keys = _transpose_header_rows(header_rows)
        _validate_column_keys(keys)


def test_insert_read_table():
    """Round-trip a flat table, including an empty cell (not stored, reads back as "")."""
    require_server()
    tie = make_client()

    headers = ["Name", "Age", "City"]
    rows = [["Alice", "30", "NYC"], ["Bob", "25", "LA"], ["Carol", "", "SF"]]

    uid = tie.insert_table("", headers, rows)
    assert uid
    got_headers, got_rows = tie.read_table(uid)
    assert got_headers == headers
    assert got_rows == rows


def test_insert_table_replace():
    """Re-inserting at the same uid replaces the table and orphans no row entities."""
    require_server()
    tie = make_client()
    uid = "pytabletest_replace_uid"

    tie.insert_table(uid, ["A", "B"], [["1", "2"], ["3", "4"], ["5", "6"]])
    old_row_uids = tie.get(uid).values(_TABLE_ROWS_REL)
    assert len(old_row_uids) == 3

    new_headers = ["X", "Y", "Z"]
    new_rows = [["a", "b", "c"], ["d", "e", "f"]]
    tie.insert_table(uid, new_headers, new_rows)

    got_headers, got_rows = tie.read_table(uid)
    assert got_headers == new_headers
    assert got_rows == new_rows

    # The first insert's row entities must be fully cleared: no cells and no
    # lingering table-row marker (an empty ghost key is acceptable).
    for r in tie.expand(_decode_ordered_list(old_row_uids)):
        assert r.attributes == {}, r.key


def test_insert_table_reserved_header():
    """A "tie-type" column is rejected up front, not left to corrupt a row's type marker."""
    with pytest.raises(ValueError):
        make_client().insert_table("", ["Name", "tie-type"], [["Alice", "person"]])


def test_insert_table_duplicate_header():
    """Duplicate column names are rejected; they would silently merge two columns' cells."""
    with pytest.raises(ValueError):
        make_client().insert_table("", ["Name", "Name"], [["Alice", "Bob"]])


def test_insert_read_table_levels():
    """Round-trip a two-row header, and check the levels-unaware reader still works."""
    require_server()
    tie = make_client()

    uid = tie.insert_table("", LEVELS_HEADER_ROWS, LEVELS_ROWS)
    got_header_rows, got_rows = tie.read_table_levels(uid)
    assert got_header_rows == LEVELS_HEADER_ROWS
    assert got_rows == LEVELS_ROWS

    got_keys, got_rows2 = tie.read_table(uid)
    assert got_keys == LEVELS_WANT_KEYS
    assert got_rows2 == LEVELS_ROWS


def test_insert_table_single_header_row_matches_flat():
    """The backward-compatibility guarantee: one header row stores exactly as a flat call.

    Existing tables therefore need no migration, and either header form may be
    passed for a single-level table without changing what lands in the store.
    """
    require_server()
    tie = make_client()

    headers = ["Name", "Age", "City"]
    rows = [["Alice", "30", "NYC"]]

    flat_uid = tie.insert_table("", headers, rows)
    levels_uid = tie.insert_table("", [headers], rows)

    flat = tie.get(flat_uid)
    via_levels = tie.get(levels_uid)
    assert (_decode_ordered_list(flat.values(_TABLE_COLUMNS_REL))
            == _decode_ordered_list(via_levels.values(_TABLE_COLUMNS_REL)))
    assert via_levels.values(_TABLE_COLUMN_LEVELS_REL) == []


def test_read_table_levels_on_flat_table():
    """A table stored without levels — every table predating the feature — is depth 1."""
    require_server()
    tie = make_client()

    headers = ["Name", "Age"]
    rows = [["Alice", "30"]]
    uid = tie.insert_table("", headers, rows)

    got_header_rows, got_rows = tie.read_table_levels(uid)
    assert got_header_rows == [headers]
    assert got_rows == rows


def test_insert_table_replace_drops_levels():
    """Re-importing a table whose header stopped being hierarchical leaves nothing stale."""
    require_server()
    tie = make_client()
    uid = "pytabletest_levels_replace_uid"

    tie.insert_table(uid, LEVELS_HEADER_ROWS, LEVELS_ROWS)
    assert tie.get(uid).values(_TABLE_COLUMN_LEVELS_REL) != []

    flat_headers = ["Sample", "Mean"]
    tie.insert_table(uid, flat_headers, [["S1", "4.3"]])
    assert tie.get(uid).values(_TABLE_COLUMN_LEVELS_REL) == []

    got_header_rows, _ = tie.read_table_levels(uid)
    assert got_header_rows == [flat_headers]


def test_delete_table_clears_levels():
    """delete_table removes column-levels too, leaving no orphaned header metadata."""
    require_server()
    tie = make_client()

    uid = tie.insert_table("", LEVELS_HEADER_ROWS, LEVELS_ROWS)
    tie.delete_table(uid)

    try:
        row = tie.get(uid)
    except t.NotFound:
        return  # fully gone is the ideal outcome
    assert row.values(_TABLE_COLUMN_LEVELS_REL) == []
    assert row.values(_TABLE_COLUMNS_REL) == []


def test_cross_client_go_writes_python_reads():
    """A two-level table written by the Go client reads back identically in Python."""
    require_server()
    tie = make_client()

    uid = go_write("pytabletest_cross_go_uid", LEVELS_HEADER_ROWS, LEVELS_ROWS)
    got_header_rows, got_rows = tie.read_table_levels(uid)
    assert got_header_rows == LEVELS_HEADER_ROWS
    assert got_rows == LEVELS_ROWS
    # Cells are keyed by the derived column key, so matching rows also proves both
    # clients derived byte-identical keys.
    assert tie.read_table(uid)[0] == LEVELS_WANT_KEYS


def test_cross_client_python_writes_go_reads():
    """A two-level table written by the Python client reads back identically in Go."""
    require_server()
    tie = make_client()

    uid = tie.insert_table("pytabletest_cross_py_uid", LEVELS_HEADER_ROWS, LEVELS_ROWS)
    got_header_rows, got_rows = go_read(uid)
    assert got_header_rows == LEVELS_HEADER_ROWS
    assert got_rows == LEVELS_ROWS


def test_cross_client_flat_table_reads_as_depth_one():
    """A table written by Go's original flat InsertTable reads as a depth-1 header."""
    require_server()
    tie = make_client()

    headers = ["Name", "Age"]
    rows = [["Alice", "30"]]
    uid = go_write("pytabletest_cross_flat_uid", headers, rows)

    got_header_rows, got_rows = tie.read_table_levels(uid)
    assert got_header_rows == [headers]
    assert got_rows == rows
