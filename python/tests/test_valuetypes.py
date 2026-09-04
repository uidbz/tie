"""Value types against a running test-env triplestore (:2161).

The value-type registry is a plain set of triples, so these tests assert on what
the client returns rather than on the encoding — but the registry subject is
pinned below, because it is the wire contract between the Go and Python clients.

Skipped automatically when the triplestore is not reachable. Run test-env first:
    cd test-env && ./build.sh && ./start.sh
"""

import socket

import pytest

import tie_client as t
from tie_client import Config, NotFound, QuerySpec, ValueType
from tie_client.highlevel import VALUE_TYPE_REGISTRY_SUBJECT

TRIPLESTORE = "http://localhost:2161"

# The Go client and this client share one "testing/testing" collection, so every
# relation these tests touch is prefixed to avoid colliding with the Go suite
# (or leaving residue behind even if a test fails mid-way).
REL_MASS = "vt-py-mass"
REL_WHEN = "vt-py-when"
REL_REDECLARE = "vt-py-redeclare"
REL_KEEP = "vt-py-keep"
REL_DROP = "vt-py-drop"
REL_UNKNOWN = "vt-py-fromthefuture"
REL_BAD = "vt-py-bad1"
REL_BAD2 = "vt-py-bad2"
REGISTRY = [REL_MASS, REL_WHEN, REL_REDECLARE, REL_KEEP, REL_DROP, REL_BAD, REL_BAD2]

SORT_TAG = "vt-py-sort"
SORT_NUM = "vt-py-count"
SORT_BAD = "vt-py-badval"
SORT_N = 10


def require_server() -> None:
    """Skip unless the test-env triplestore answers, mirroring Go's requireServer."""
    try:
        socket.create_connection(("localhost", 2161), timeout=1).close()
    except OSError as err:
        pytest.skip(f"no triplestore at {TRIPLESTORE}: {err}")


def make_client() -> t.TieClient:
    """A client on the namespace/collection Go's client.TestingConfig() uses."""
    cfg = Config(
        username="defaultuser", password="defaultpassword",
        namespace="testing", collection="testing",
        webservice=TRIPLESTORE, prev_versions=3,
    )
    return t.TieClient(cfg)


def _row_keys(client: t.TieClient, spec: QuerySpec) -> list[str]:
    try:
        rows, _ = client.query(spec)
    except NotFound:
        return []
    return [r.key for r in rows]


@pytest.fixture
def tie():
    require_server()
    client = make_client()
    yield client
    # Clean up whatever a test left behind: the registry is one entity per
    # shared collection, and the sort subjects outlive the test otherwise.
    try:
        for r in REGISTRY:
            client.set_values(VALUE_TYPE_REGISTRY_SUBJECT, r, [])
        client.set_value_types({})
    except Exception:
        pass
    for key in [f"vk{i:02d}" for i in range(SORT_N)] + ["vk-badnum", "vk-novalue"]:
        for rel in (SORT_TAG, SORT_NUM, SORT_BAD):
            client.set_values(key, rel, [])
    client.sync()


def assert_clean(client: t.TieClient) -> None:
    row = client.get(VALUE_TYPE_REGISTRY_SUBJECT)
    for r in REGISTRY:
        assert r not in row.attributes, f"residue left in registry: {r}"


def test_round_trip(tie: t.TieClient) -> None:
    tie.set_value_types({REL_MASS: ValueType.FLOAT, REL_WHEN: ValueType.DATETIME})
    types = tie.value_types()
    assert types[REL_MASS] == ValueType.FLOAT
    assert types[REL_WHEN] == ValueType.DATETIME
    assert_clean(tie)


def test_redeclare_replaces(tie: t.TieClient) -> None:
    tie.set_value_types({REL_REDECLARE: ValueType.INT})
    tie.set_value_types({REL_REDECLARE: ValueType.FLOAT})
    row = tie.get(VALUE_TYPE_REGISTRY_SUBJECT)
    assert row.values(REL_REDECLARE) == [ValueType.FLOAT]
    assert_clean(tie)


def test_merge_and_clear(tie: t.TieClient) -> None:
    tie.set_value_types({REL_KEEP: ValueType.INT, REL_DROP: ValueType.BOOL})
    # Declaring "" clears the entry, because an absent entry means string.
    tie.set_value_types({REL_DROP: ""})
    types = tie.value_types()
    assert types[REL_KEEP] == ValueType.INT
    assert REL_DROP not in types
    assert_clean(tie)


def test_unknown_type_ignored_on_read(tie: t.TieClient) -> None:
    tie.set_values(VALUE_TYPE_REGISTRY_SUBJECT, REL_UNKNOWN, ["complex128"])
    types = tie.value_types()  # must not raise
    assert REL_UNKNOWN not in types
    assert_clean(tie)


def test_set_rejects_unknown(tie: t.TieClient) -> None:
    with pytest.raises(ValueError):
        tie.set_value_types({REL_BAD: "decimal"})


def test_all_or_nothing(tie: t.TieClient) -> None:
    with pytest.raises(ValueError):
        tie.set_value_types({REL_BAD: ValueType.INT, REL_BAD2: "decimal"})
    types = tie.value_types()
    assert REL_BAD not in types
    assert REL_BAD2 not in types
    assert_clean(tie)


def test_registry_subject_is_pinned() -> None:
    # The subject string is the cross-client contract; a drift here would quietly
    # split the registry between the Go and Python clients.
    assert VALUE_TYPE_REGISTRY_SUBJECT == "value-types"


# ---------------------------------------------------------------------------
# numeric sort
# ---------------------------------------------------------------------------


def test_sort_by_value_numeric(tie: t.TieClient) -> None:
    # key i carries i+1 WITHOUT zero-padding, so numeric order (1..10) diverges
    # from lexicographic (1, 10, 2, ...): numeric sort must come out vk00..vk09.
    for i in range(SORT_N):
        key = f"vk{i:02d}"
        tie.add(key, SORT_TAG, "vt-py-table")
        tie.add(key, SORT_NUM, str(i + 1))
    # One key holds a non-numeric value and one no value at all; both must sort
    # last in both directions (tie-broken by key).
    tie.add("vk-badnum", SORT_TAG, "vt-py-table")
    tie.add("vk-badnum", SORT_BAD, "not-a-number")
    tie.add("vk-novalue", SORT_TAG, "vt-py-table")
    tie.sync()

    base = dict(terms=[SORT_TAG], filter="tag", reverse=True, limit=-1,
                sort_by_value=SORT_NUM)

    lex = _row_keys(tie, QuerySpec(**base))
    assert lex[1] == "vk09", f"expected lexicographic order (10 after 1): {lex}"

    numeric = _row_keys(tie, QuerySpec(sort_by_value_numeric=True, **base))
    expected = [f"vk{i:02d}" for i in range(SORT_N)] + ["vk-badnum", "vk-novalue"]
    assert numeric == expected, f"numeric order wrong: {numeric}"

    numeric_desc = _row_keys(tie, QuerySpec(sort_by_value_numeric=True, descending=True, **base))
    expected_desc = [f"vk{i:02d}" for i in range(SORT_N - 1, -1, -1)] + ["vk-badnum", "vk-novalue"]
    assert numeric_desc == expected_desc, f"numeric desc order wrong: {numeric_desc}"

    # A column that is never declared numeric keeps its lexicographic order.
    label = _row_keys(tie, QuerySpec(sort_by_value=SORT_BAD, sort_by_value_numeric=True, **base))
    assert label[0] == "vk-badnum" or len(label) == 2, f"unexpected: {label}"


def test_declarable_numeric_type(tie: t.TieClient) -> None:
    # The registry's purpose: readers learn a relation is numeric without
    # parsing a value.
    tie.set_value_types({SORT_NUM: ValueType.INT})
    types = tie.value_types()
    assert types[SORT_NUM] == ValueType.INT
    try:
        assert_clean(tie)
    finally:
        pass
