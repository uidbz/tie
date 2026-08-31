"""Request-envelope field names/casing must match api/*.go exactly."""

from tie_client import Config, QuerySpec, TieClient, Update


def _client_capturing(captured):
    cfg = Config(namespace="NS", collection="COL", webservice="http://localhost:1",
                 username="u", password="p")
    tie = TieClient(cfg)

    def fake_run(request_id, body):
        captured.append((request_id, body))
        return {"Success": True, "rows": [], "totalCount": 0, "tags": []}

    tie._triplestore.run = fake_run
    return tie


def test_add_envelope():
    cap = []
    tie = _client_capturing(cap)
    tie.add("k", "v1", "v2")
    rid, body = cap[-1]
    assert rid == "Add"
    assert body == {"Id": "Add", "Namespace": "NS", "CollectionId": "COL",
                    "Key": "k", "Value1": "v1", "Value2": "v2"}


def test_set_envelope_lowercase():
    cap = []
    tie = _client_capturing(cap)
    tie.set_values("k", "rel", ["a", "b"])
    rid, body = cap[-1]
    assert rid == "Set"
    # Set uses lowercase-tagged fields (api/set.go).
    assert body["key"] == "k" and body["relation"] == "rel" and body["values"] == ["a", "b"]
    assert body["Namespace"] == "NS" and body["CollectionId"] == "COL"


def test_query_envelope():
    cap = []
    tie = _client_capturing(cap)
    tie.query(QuerySpec(terms=["t"], exclude=["x"], scope="s", missing_relation="tag",
                        filter="f", reverse=True, expand=True, offset=5, limit=10, sort_by="name"))
    rid, body = cap[-1]
    assert rid == "Query"
    assert body["terms"] == ["t"] and body["exclude"] == ["x"] and body["scope"] == "s"
    assert body["missingRelation"] == "tag" and body["filter"] == "f"
    assert body["reverse"] is True and body["expand"] is True
    assert body["sort"] == {"Offset": 5, "Limit": 10, "SortBy": "name"}


def test_batch_envelope():
    cap = []
    tie = _client_capturing(cap)
    b = tie.new_batch()
    b.add("k", "r", "v")
    b.delete("k", "r", "w")
    b.set("k", "r", ["x"])
    b.update(Update("k", "r", "old", "new", add_on_failure=True))
    tie.run_batch(b)
    rid, body = cap[-1]
    assert rid == "Batch"
    assert body["batch"]["collection"] == {"Namespace": "NS", "CollectionId": "COL"}
    ops = body["batch"]["ops"]
    assert ops[0] == {"op": "add", "key": "k", "relation": "r", "values": ["v"]}
    assert ops[1] == {"op": "delete", "key": "k", "relation": "r", "values": ["w"]}
    assert ops[2] == {"op": "set", "key": "k", "relation": "r", "values": ["x"]}
    assert ops[3] == {"op": "update", "key": "k", "relation": "r", "values": ["old"],
                      "newValue": "new", "addOnFailure": True}


def test_collection_override():
    cap = []
    tie = _client_capturing(cap)
    tie.add("k", "v1", "v2", collection="Other")
    _, body = cap[-1]
    assert body["CollectionId"] == "Other"
