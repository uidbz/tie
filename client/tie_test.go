package client

import (
	"errors"
	"net"
	"strings"
	"testing"
)

// requireServer skips the test unless a tie-daemon is reachable on the
// configured Webservice. These are integration tests over real HTTP+JSON; run
// them against the test-env daemon (test-env/start.sh).
func requireServer(t *testing.T, tie *TieClient) {
	t.Helper()
	addr := tie.Config.Webservice
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Skipf("no daemon at %s: %v", tie.Config.Webservice, err)
	}
	conn.Close()
}

func TestAdd(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)
	if _, err := tie.Add("heyhey", "Noice", "Oh yeah!"); err != nil {
		t.Error(err)
	}
}

func TestQuery(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)
	if _, err := tie.Add("heyhey", "Noice", "Oh yeah!"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}
	rows, _, err := tie.Query(QuerySpec{Terms: []string{"heyhey"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "heyhey" {
		t.Fatalf("Query = %v, want one row keyed heyhey", rows)
	}
	if !RowHas(rows[0], "Noice", "Oh yeah!") {
		t.Errorf("expected value not found in %v", rows[0].Attributes)
	}
}

func TestGet(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)
	if _, err := tie.Add("attrskey", "color", "blue"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}
	row, err := tie.Get("attrskey")
	if err != nil {
		t.Fatal(err)
	}
	if got := RowValues(row, "color"); len(got) != 1 || got[0] != "blue" {
		t.Errorf("RowValues(color) = %v, want [blue]", got)
	}
}

func TestDelete(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)
	if _, err := tie.Add("heyhey", "Noice", "Oh yeah!"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}
	if _, err := tie.Delete("heyhey", "Noice", "Oh yeah!"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}
	row, err := tie.Get("heyhey")
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Error(err)
	}
	if RowHas(row, "Noice", "Oh yeah!") {
		t.Error("value should have been deleted")
	}
}

func TestUpdate(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	if _, err := tie.Add("Heyhey", "Noice", "Oh yeah"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}

	update := tie.NewUpdate("Heyhey", "Noice", "Oh yeah", "Oh yeah 2")
	if _, err := tie.Update(update); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}

	row, err := tie.Get("Heyhey")
	if err != nil {
		t.Fatal(err)
	}
	if !RowHas(row, "Noice", "Oh yeah 2") {
		t.Error("new value not found")
	}
	if RowHas(row, "Noice", "Oh yeah") {
		t.Error("old value should be gone")
	}

	if _, err := tie.Delete("Heyhey", "Noice", "Oh yeah 2"); err != nil {
		t.Error(err)
	}
}

// TestSet verifies the server-side replace semantics: Set makes (key, relation)
// hold exactly the given values, dropping any prior ones in one op.
func TestSet(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	if _, err := tie.Add("setkey", "tag", "old1"); err != nil {
		t.Error(err)
	}
	if _, err := tie.Add("setkey", "tag", "old2"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}

	if err := tie.Set("setkey", "tag", []string{"new1", "new2"}); err != nil {
		t.Fatal(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}

	row, err := tie.Get("setkey")
	if err != nil {
		t.Fatal(err)
	}
	got := RowValues(row, "tag")
	if len(got) != 2 || !RowHas(row, "tag", "new1") || !RowHas(row, "tag", "new2") {
		t.Errorf("after Set, tag = %v, want [new1 new2]", got)
	}
	if RowHas(row, "tag", "old1") || RowHas(row, "tag", "old2") {
		t.Error("old values should have been replaced by Set")
	}

	// Empty values clears the relation.
	if err := tie.Set("setkey", "tag", nil); err != nil {
		t.Fatal(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}
	row, err = tie.Get("setkey")
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if len(RowValues(row, "tag")) != 0 {
		t.Errorf("after Set(nil), tag = %v, want empty", RowValues(row, "tag"))
	}
}

// TestExpand verifies the multi-key batch fetch returns one Row per existing key.
func TestExpand(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	b := tie.NewBatch()
	b.Add("exp1", "n", "1")
	b.Add("exp2", "n", "2")
	if _, err := tie.Batch(b); err != nil {
		t.Fatal(err)
	}

	rows, err := tie.Expand([]string{"exp1", "exp2", "exp-missing"})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Row{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if _, ok := byKey["exp-missing"]; ok {
		t.Error("missing key should be omitted from Expand result")
	}
	if v := RowFirst(byKey["exp1"], "n"); v != "1" {
		t.Errorf("exp1.n = %q, want 1", v)
	}
	if v := RowFirst(byKey["exp2"], "n"); v != "2" {
		t.Errorf("exp2.n = %q, want 2", v)
	}
}

// TestBatchArrayOrder verifies ops run in slice order: an add followed by a
// delete of the same triple leaves it gone; the reverse order leaves it present.
func TestBatchArrayOrder(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	// add-then-delete => absent.
	b := tie.NewBatch()
	b.Add("orderkey", "x", "v")
	b.Delete("orderkey", "x", "v")
	if _, err := tie.Batch(b); err != nil {
		t.Fatal(err)
	}
	row, err := tie.Get("orderkey")
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if RowHas(row, "x", "v") {
		t.Error("add-then-delete should leave the triple absent")
	}

	// delete(no-op)-then-add => present (delete of a missing triple is not a failure).
	b = tie.NewBatch()
	b.Delete("orderkey", "x", "v")
	b.Add("orderkey", "x", "v")
	if _, err := tie.Batch(b); err != nil {
		t.Fatal(err)
	}
	row, err = tie.Get("orderkey")
	if err != nil {
		t.Fatal(err)
	}
	if !RowHas(row, "x", "v") {
		t.Error("delete-then-add should leave the triple present")
	}

	// cleanup
	tie.Delete("orderkey", "x", "v")
	tie.Sync()
}
