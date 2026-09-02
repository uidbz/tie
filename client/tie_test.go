package client

import (
	"errors"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// requireServer skips the test unless a tie-triplestore is reachable on the
// configured TripleStoreURL. These are integration tests over real HTTP+JSON;
// run them against the test-env triplestore (test-env/start.sh).
func requireServer(t *testing.T, tie *TieClient) {
	t.Helper()
	addr := tie.Config.TripleStoreURL
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Skipf("no triplestore at %s: %v", tie.Config.TripleStoreURL, err)
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

// TestInsertReadTable round-trips a table, including an empty cell (which is not
// stored but must read back as "").
func TestInsertReadTable(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	headers := []string{"Name", "Age", "City"}
	rows := [][]string{
		{"Alice", "30", "NYC"},
		{"Bob", "25", "LA"},
		{"Carol", "", "SF"}, // empty cell
	}

	uid, err := tie.InsertTable("", headers, rows)
	if err != nil {
		t.Fatal(err)
	}
	if uid == "" {
		t.Fatal("InsertTable returned empty uid")
	}

	gotH, gotR, err := tie.ReadTable(uid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotH, headers) {
		t.Errorf("headers = %v, want %v", gotH, headers)
	}
	if !reflect.DeepEqual(gotR, rows) {
		t.Errorf("rows = %v, want %v", gotR, rows)
	}
}

// TestInsertTableReplace verifies that re-inserting at the same uid replaces the
// table in place and leaves no orphaned row entities behind.
func TestInsertTableReplace(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	const uid = "tabletest_replace_uid"

	if _, err := tie.InsertTable(uid, []string{"A", "B"}, [][]string{
		{"1", "2"}, {"3", "4"}, {"5", "6"},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := tie.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	oldRowUIDs := RowValues(first, tableRowsRel)
	if len(oldRowUIDs) != 3 {
		t.Fatalf("first insert = %d rows, want 3", len(oldRowUIDs))
	}

	newHeaders := []string{"X", "Y", "Z"}
	newRows := [][]string{{"a", "b", "c"}, {"d", "e", "f"}}
	if _, err := tie.InsertTable(uid, newHeaders, newRows); err != nil {
		t.Fatal(err)
	}

	gotH, gotR, err := tie.ReadTable(uid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotH, newHeaders) {
		t.Errorf("headers = %v, want %v", gotH, newHeaders)
	}
	if !reflect.DeepEqual(gotR, newRows) {
		t.Errorf("rows = %v, want %v", gotR, newRows)
	}

	// The first insert's row entities must be fully cleared: no cells and no
	// lingering table-row type marker (an empty ghost key is acceptable).
	orphans, err := tie.Expand(oldRowUIDs)
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, r := range orphans {
		if len(r.Attributes) != 0 {
			t.Errorf("old row %s not cleared: %v", r.Key, r.Attributes)
		}
	}
}

// TestInsertTableReservedHeader verifies a "tie-type" column is rejected up
// front rather than silently corrupting a row entity's type marker.
func TestInsertTableReservedHeader(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	_, err := tie.InsertTable("", []string{"Name", "tie-type"}, [][]string{{"Alice", "person"}})
	if err == nil {
		t.Fatal("expected error for reserved 'tie-type' header, got nil")
	}
}

// TestInsertTableDuplicateHeader verifies duplicate column names are rejected
// (they would otherwise silently merge cells under one relation).
func TestInsertTableDuplicateHeader(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	_, err := tie.InsertTable("", []string{"Name", "Name"}, [][]string{{"Alice", "Bob"}})
	if err == nil {
		t.Fatal("expected error for duplicate header, got nil")
	}
}

// The two-row header used by most of the levels tests: a plain index column
// beside two temperature groups, each holding two replicates. Note the repeated
// parent labels — a merged parent cell is forward-filled by the caller.
var (
	levelsHeaderRows = [][]string{
		{"Sample", "Temperature (20°C)", "Temperature (20°C)", "Temperature (37°C)", "Temperature (37°C)"},
		{"", "Replicate 1", "Replicate 2", "Replicate 1", "Replicate 2"},
	}
	levelsRows = [][]string{
		{"S1", "4.2", "4.4", "5.9", "6.1"},
		{"S2", "4.1", "", "6.0", "6.3"}, // empty cell
	}
	levelsWantKeys = []string{
		"Sample",
		"Temperature (20°C)\x1fReplicate 1",
		"Temperature (20°C)\x1fReplicate 2",
		"Temperature (37°C)\x1fReplicate 1",
		"Temperature (37°C)\x1fReplicate 2",
	}
)

// TestHeaderLevelTranspose covers the pure header helpers, which need no server:
// row-major header rows in, per-column levels and derived keys out, and back.
func TestHeaderLevelTranspose(t *testing.T) {
	levels, keys, err := transposeHeaderRows(levelsHeaderRows)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, levelsWantKeys) {
		t.Errorf("keys = %q, want %q", keys, levelsWantKeys)
	}
	// An index column's blank lower level is dropped from the key but kept in the
	// levels, so the column still reports its true depth.
	if want := []string{"Sample", ""}; !slices.Equal(levels[0], want) {
		t.Errorf("levels[0] = %q, want %q", levels[0], want)
	}
	if got := transposeLevels(levels); !reflect.DeepEqual(got, levelsHeaderRows) {
		t.Errorf("round trip = %q, want %q", got, levelsHeaderRows)
	}
}

// TestHeaderLevelsRejected covers the header shapes storage cannot represent.
func TestHeaderLevelsRejected(t *testing.T) {
	cases := map[string][][]string{
		"not rectangular":    {{"A", "B"}, {"x"}},
		"reserved separator": {{"A", "B\x1fC"}},
		"column with no label": {
			{"A", ""},
			{"B", ""},
		},
		"levels deriving a duplicate key": {
			{"Model", "Model"},
			{"p", "p"},
		},
	}
	for name, headerRows := range cases {
		t.Run(name, func(t *testing.T) {
			levels, keys, err := transposeHeaderRows(headerRows)
			if err != nil {
				return // rejected in the transpose, as expected
			}
			if err := validateColumnKeys(keys); err == nil {
				t.Errorf("accepted %q (levels %q, keys %q), want error", headerRows, levels, keys)
			}
		})
	}
}

// TestInsertReadTableLevels round-trips a table with a two-row header.
func TestInsertReadTableLevels(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	uid, err := tie.InsertTableLevels("", levelsHeaderRows, levelsRows)
	if err != nil {
		t.Fatal(err)
	}

	gotHeaderRows, gotRows, err := tie.ReadTableLevels(uid)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotHeaderRows, levelsHeaderRows) {
		t.Errorf("headerRows = %q, want %q", gotHeaderRows, levelsHeaderRows)
	}
	if !reflect.DeepEqual(gotRows, levelsRows) {
		t.Errorf("rows = %q, want %q", gotRows, levelsRows)
	}

	// ReadTable is unaware of levels and must still work, yielding the keys.
	gotKeys, gotRows2, err := tie.ReadTable(uid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotKeys, levelsWantKeys) {
		t.Errorf("ReadTable headers = %q, want %q", gotKeys, levelsWantKeys)
	}
	if !reflect.DeepEqual(gotRows2, levelsRows) {
		t.Errorf("ReadTable rows = %q, want %q", gotRows2, levelsRows)
	}
}

// TestInsertTableLevelsSingleRowMatchesFlat is the backward-compatibility
// guarantee: a one-row header must be stored exactly as InsertTable stores it, so
// existing tables need no migration and either writer is interchangeable.
func TestInsertTableLevelsSingleRowMatchesFlat(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	headers := []string{"Name", "Age", "City"}
	rows := [][]string{{"Alice", "30", "NYC"}}

	flatUID, err := tie.InsertTable("", headers, rows)
	if err != nil {
		t.Fatal(err)
	}
	levelsUID, err := tie.InsertTableLevels("", [][]string{headers}, rows)
	if err != nil {
		t.Fatal(err)
	}

	flat, err := tie.Get(flatUID)
	if err != nil {
		t.Fatal(err)
	}
	viaLevels, err := tie.Get(levelsUID)
	if err != nil {
		t.Fatal(err)
	}

	flatColumns := decodeOrderedList(RowValues(flat, tableColumnsRel))
	levelsColumns := decodeOrderedList(RowValues(viaLevels, tableColumnsRel))
	if !slices.Equal(flatColumns, levelsColumns) {
		t.Errorf("columns differ: flat %q, via levels %q", flatColumns, levelsColumns)
	}
	if got := RowValues(viaLevels, tableColumnLevelsRel); len(got) != 0 {
		t.Errorf("a single-row header stored column-levels %q, want none", got)
	}
}

// TestReadTableLevelsOnFlatTable verifies a table written without levels — every
// table predating this feature — reads back as a depth-1 header rather than empty.
func TestReadTableLevelsOnFlatTable(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	headers := []string{"Name", "Age"}
	rows := [][]string{{"Alice", "30"}}
	uid, err := tie.InsertTable("", headers, rows)
	if err != nil {
		t.Fatal(err)
	}

	gotHeaderRows, gotRows, err := tie.ReadTableLevels(uid)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{headers}; !reflect.DeepEqual(gotHeaderRows, want) {
		t.Errorf("headerRows = %q, want %q", gotHeaderRows, want)
	}
	if !reflect.DeepEqual(gotRows, rows) {
		t.Errorf("rows = %q, want %q", gotRows, rows)
	}
}

// TestInsertTableLevelsReplaceDropsLevels verifies that re-importing a table whose
// header stopped being hierarchical leaves no stale column-levels behind.
func TestInsertTableLevelsReplaceDropsLevels(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	const uid = "tabletest_levels_replace_uid"

	if _, err := tie.InsertTableLevels(uid, levelsHeaderRows, levelsRows); err != nil {
		t.Fatal(err)
	}
	row, err := tie.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := RowValues(row, tableColumnLevelsRel); len(got) == 0 {
		t.Fatal("first insert stored no column-levels")
	}

	flatHeaders := []string{"Sample", "Mean"}
	if _, err := tie.InsertTable(uid, flatHeaders, [][]string{{"S1", "4.3"}}); err != nil {
		t.Fatal(err)
	}
	row, err = tie.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got := RowValues(row, tableColumnLevelsRel); len(got) != 0 {
		t.Errorf("column-levels = %q after flat re-import, want none", got)
	}

	gotHeaderRows, _, err := tie.ReadTableLevels(uid)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{flatHeaders}; !reflect.DeepEqual(gotHeaderRows, want) {
		t.Errorf("headerRows = %q, want %q", gotHeaderRows, want)
	}
}

// TestDeleteTableClearsLevels verifies DeleteTable removes column-levels too, so a
// deleted table leaves no orphaned header metadata.
func TestDeleteTableClearsLevels(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	uid, err := tie.InsertTableLevels("", levelsHeaderRows, levelsRows)
	if err != nil {
		t.Fatal(err)
	}
	if err := tie.DeleteTable(uid); err != nil {
		t.Fatal(err)
	}

	row, err := tie.Get(uid)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return // fully gone is the ideal outcome
		}
		t.Fatal(err)
	}
	if got := RowValues(row, tableColumnLevelsRel); len(got) != 0 {
		t.Errorf("column-levels = %q after delete, want none", got)
	}
}

func TestFavorites(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	if err := tie.RegisterFavorite("blue"); err != nil {
		t.Fatal(err)
	}
	if err := tie.RegisterFavorite("amber"); err != nil {
		t.Fatal(err)
	}

	got, err := tie.ListFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"amber", "blue"}; !slices.Equal(got, want) {
		t.Errorf("ListFavorites = %v, want %v (sorted)", got, want)
	}

	if err := tie.UnregisterFavorite("blue"); err != nil {
		t.Fatal(err)
	}
	got, err = tie.ListFavorites()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"amber"}; !slices.Equal(got, want) {
		t.Errorf("after unregister, ListFavorites = %v, want %v", got, want)
	}

	// cleanup
	tie.UnregisterFavorite("amber")
}
