package client

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/uidbz/tie/api"
)

// A table is a client-side convenience over the triple store: a rectangular
// grid of string cells with ordered column headers, built entirely on the
// existing Query/Set/Get/Expand/Batch primitives. There is no server-side table
// type — the triplestore still sees only triples.
//
// A table entity T carries its column and row order as lists:
//
//	(T, "tie-type", "table")
//	(T, "columns",  [<ord>|h0, <ord>|h1, …])   // column order
//	(T, "rows",     [<ord>|R0, <ord>|R1, …])   // row order (row-entity uids)
//
// Each row entity Ri holds its cells, one triple per non-empty cell keyed by
// its column header:
//
//	(Ri, "tie-type", "table-row")
//	(Ri, <header>,   <cell>)
//
// A table may also carry a multi-row (hierarchical) header, where each column has
// an ordered list of header levels instead of a single label:
//
//	(T, "column-levels", [<ord>|lvl0<sep>lvl1, <ord>|lvl0<sep>lvl1, …])
//
// Because a header keys every cell triple in its column, each column still needs
// exactly one unique string. A column's key is its non-empty levels joined by
// levelSep, and that is what "columns" holds; dropping empty levels means a
// single-row header keys as the label itself, so such tables are stored
// identically however they were written. Levels cannot be recovered from a key
// alone (empty levels are gone, so depth is not encoded), hence both relations.
// The relation is absent on single-level tables and ReadTable ignores it, so
// hierarchical headers are purely additive: older readers see the flat keys.
//
// The store returns a subject's multi-value attributes sorted, not in insertion
// order, so each entry in the columns/rows lists is prefixed with a zero-padded
// ordinal: the server's lexicographic sort then reproduces the original order.
// Reads are a forward Get(T) + Expand(rowUIDs) — no reverse-index dependency and
// no triplestore config. The trade-off is that "rows" is a single multi-value
// attribute, so this targets sheet-sized tables (hundreds to low thousands of
// rows), not very large ones. Headers must be unique and none may be named
// "tie-type" (it would collide with a row entity's type marker); either case is
// rejected with an error.
const (
	tableColumnsRel      = "columns"
	tableRowsRel         = "rows"
	tableColumnLevelsRel = "column-levels"
	tieTypeTable         = "table"
	tieTypeTableRow      = "table-row"

	// orderSep separates the ordinal prefix from the payload in a list entry.
	// NUL is used so it will not appear in a header or a hex row uid.
	orderSep = "\x00"

	// levelSep separates one column's header levels, both in a column-levels entry
	// and in the derived column key. Unit Separator is used because it will not
	// appear in a header label; orderSep is unavailable, it already delimits the
	// ordinal prefix.
	levelSep = "\x1f"
)

func encodeOrdered(i int, s string) string {
	return fmt.Sprintf("%08d%s%s", i, orderSep, s)
}

// decodeOrderedList parses ordinal-prefixed list values and returns their
// payloads in ordinal order, independent of the order values arrive in.
func decodeOrderedList(values []string) []string {
	type item struct {
		i int
		s string
	}
	items := make([]item, 0, len(values))
	for _, v := range values {
		idx, payload, ok := strings.Cut(v, orderSep)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(idx)
		if err != nil {
			continue
		}
		items = append(items, item{n, payload})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].i < items[b].i })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.s
	}
	return out
}

// columnKey joins a column's non-empty header levels into the single unique
// string that keys every cell triple in that column.
func columnKey(levels []string) string {
	parts := make([]string, 0, len(levels))
	for _, l := range levels {
		if l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, levelSep)
}

// transposeHeaderRows converts row-major header rows (headerRows[i][j] is level i
// of column j, matching how a spreadsheet reads) into per-column level lists plus
// each column's derived key.
func transposeHeaderRows(headerRows [][]string) (levels [][]string, keys []string, err error) {
	if len(headerRows) == 0 {
		return nil, nil, nil
	}
	width := len(headerRows[0])
	for i, hr := range headerRows {
		if len(hr) != width {
			return nil, nil, fmt.Errorf("client: header row %d has %d columns, want %d; header rows must be rectangular", i, len(hr), width)
		}
	}
	levels = make([][]string, width)
	keys = make([]string, width)
	for j := 0; j < width; j++ {
		col := make([]string, len(headerRows))
		for i, hr := range headerRows {
			if strings.ContainsAny(hr[j], levelSep+orderSep) {
				return nil, nil, fmt.Errorf("client: header level %q (row %d, column %d) contains a reserved separator", hr[j], i, j)
			}
			col[i] = hr[j]
		}
		if columnKey(col) == "" {
			return nil, nil, fmt.Errorf("client: column %d has no non-empty header level", j)
		}
		levels[j] = col
		keys[j] = columnKey(col)
	}
	return levels, keys, nil
}

// transposeLevels converts per-column level lists back into row-major header
// rows, padding columns of unequal depth with trailing empty levels so the result
// is rectangular.
func transposeLevels(levels [][]string) [][]string {
	depth := 0
	for _, col := range levels {
		if len(col) > depth {
			depth = len(col)
		}
	}
	if depth == 0 {
		return nil
	}
	headerRows := make([][]string, depth)
	for i := range headerRows {
		hr := make([]string, len(levels))
		for j, col := range levels {
			if i < len(col) {
				hr[j] = col[i]
			}
		}
		headerRows[i] = hr
	}
	return headerRows
}

// decodeColumnLevels returns per-column header levels. A table stored without a
// column-levels relation — every table written before hierarchical headers
// existed, and every single-level table since — reads back as one level per
// column equal to its key, so both kinds share a single read path.
func decodeColumnLevels(values []string, keys []string) [][]string {
	entries := decodeOrderedList(values)
	levels := make([][]string, len(keys))
	for j, key := range keys {
		if j < len(entries) {
			levels[j] = strings.Split(entries[j], levelSep)
			continue
		}
		levels[j] = []string{key}
	}
	return levels
}

// validateColumnKeys rejects the two keys the storage layout cannot represent: the
// row entities' own type marker, and duplicates (a column key is a predicate, so
// two identical keys would write both columns' cells to one triple).
func validateColumnKeys(keys []string) error {
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k == str(TieTypeProperty) {
			return fmt.Errorf("client: column name %q is reserved and cannot be used as a table header", k)
		}
		if _, dup := seen[k]; dup {
			return fmt.Errorf("client: duplicate column name %q; table headers must be unique", k)
		}
		seen[k] = struct{}{}
	}
	return nil
}

// InsertTable writes headers + rows as a table entity and returns its uid. An
// empty uid mints a fresh one; a non-empty uid replaces the table already stored
// there (idempotent re-import) — its old row entities are cleared first. rows
// are row-major; each inner slice holds one row's cells in header order. Short
// rows are padded with empty cells and cells past len(headers) are ignored.
// Empty cells are not stored (ReadTable reconstructs them as "").
func (tc *TieClient) InsertTable(uid string, headers []string, rows [][]string) (string, error) {
	if err := validateColumnKeys(headers); err != nil {
		return "", err
	}
	return tc.insertTable(uid, headers, nil, rows)
}

// InsertTableLevels writes a table whose header spans several rows. headerRows is
// row-major — headerRows[i][j] is level i of column j — matching the layout of the
// source sheet; it must be rectangular, with merged parent cells already
// forward-filled and blanks explicit (deciding where a merged cell ends is
// file-parsing work that belongs to the caller, not to storage). Each column's key
// is its non-empty levels joined, and those keys must be unique. Otherwise it
// behaves exactly like InsertTable.
//
// A single header row is stored identically to the equivalent InsertTable call, so
// depth 1 is not a special case for callers.
func (tc *TieClient) InsertTableLevels(uid string, headerRows [][]string, rows [][]string) (string, error) {
	levels, keys, err := transposeHeaderRows(headerRows)
	if err != nil {
		return "", err
	}
	if err := validateColumnKeys(keys); err != nil {
		return "", err
	}
	if len(headerRows) < 2 {
		levels = nil
	}
	return tc.insertTable(uid, keys, levels, rows)
}

// insertTable is the shared writer. levels may be nil, meaning a single-level
// header: the column-levels relation is then cleared rather than written, so a
// re-import that drops a hierarchy leaves nothing stale behind.
func (tc *TieClient) insertTable(uid string, keys []string, levels [][]string, rows [][]string) (string, error) {
	minted := uid == ""
	if minted {
		uid = string(tc.newDirUID())
	}

	batch := tc.NewBatch()

	if !minted {
		if err := tc.appendClearTable(batch, uid); err != nil {
			return "", err
		}
	}

	columns := make([]string, len(keys))
	for j, key := range keys {
		columns[j] = encodeOrdered(j, key)
	}
	batch.Set(uid, str(TieTypeProperty), []string{tieTypeTable})
	batch.Set(uid, tableColumnsRel, columns)

	var levelEntries []string
	if levels != nil {
		levelEntries = make([]string, len(levels))
		for j, col := range levels {
			levelEntries[j] = encodeOrdered(j, strings.Join(col, levelSep))
		}
	}
	batch.Set(uid, tableColumnLevelsRel, levelEntries)

	rowRefs := make([]string, len(rows))
	for i, cells := range rows {
		rowUID := string(tc.newDirUID())
		rowRefs[i] = encodeOrdered(i, rowUID)
		batch.Set(rowUID, str(TieTypeProperty), []string{tieTypeTableRow})
		for j, key := range keys {
			if j < len(cells) && cells[j] != "" {
				batch.Add(rowUID, key, cells[j])
			}
		}
	}
	batch.Set(uid, tableRowsRel, rowRefs)

	if _, err := tc.Batch(batch); err != nil {
		return "", err
	}
	return uid, nil
}

// ReadTable returns a table's headers and rows (row-major, header order), or
// ErrNotFound if uid holds no table entity. On a table with a multi-row header the
// headers are the derived column keys; use ReadTableLevels to get the levels.
func (tc *TieClient) ReadTable(uid string) (headers []string, rows [][]string, err error) {
	headers, _, rows, err = tc.readTable(uid)
	return headers, rows, err
}

// ReadTableLevels returns a table's header levels as row-major header rows, in the
// same shape InsertTableLevels accepts, plus its rows. A table stored with a
// single-row header yields exactly one header row, so callers need not distinguish
// the two cases.
func (tc *TieClient) ReadTableLevels(uid string) (headerRows [][]string, rows [][]string, err error) {
	_, levels, rows, err := tc.readTable(uid)
	if err != nil {
		return nil, nil, err
	}
	return transposeLevels(levels), rows, nil
}

// readTable is the shared reader: one forward Get plus one Expand serves both
// public readers, so asking for levels costs no extra round trip.
func (tc *TieClient) readTable(uid string) (keys []string, levels [][]string, rows [][]string, err error) {
	row, err := tc.Get(uid)
	if err != nil {
		return nil, nil, nil, err
	}
	keys = decodeOrderedList(RowValues(row, tableColumnsRel))
	levels = decodeColumnLevels(RowValues(row, tableColumnLevelsRel), keys)
	rowUIDs := decodeOrderedList(RowValues(row, tableRowsRel))
	if len(rowUIDs) == 0 {
		return keys, levels, nil, nil
	}

	expanded, err := tc.Expand(rowUIDs)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, nil, nil, err
	}
	// Expand omits row uids with no stored cells (all-empty rows), so index the
	// results by key and walk rowUIDs to keep row order and alignment intact.
	byKey := make(map[string]Row, len(expanded))
	for _, r := range expanded {
		byKey[r.Key] = r
	}

	rows = make([][]string, len(rowUIDs))
	for i, rowUID := range rowUIDs {
		r := byKey[rowUID]
		cells := make([]string, len(keys))
		for j, key := range keys {
			cells[j] = RowFirst(r, key)
		}
		rows[i] = cells
	}
	return keys, levels, rows, nil
}

// DeleteTable removes a table and all its row entities. It is idempotent:
// deleting a missing or already-deleted table is a no-op that returns nil.
func (tc *TieClient) DeleteTable(uid string) error {
	batch := tc.NewBatch()
	if err := tc.appendClearTable(batch, uid); err != nil {
		return err
	}
	batch.Set(uid, tableColumnsRel, nil)
	batch.Set(uid, tableColumnLevelsRel, nil)
	batch.Set(uid, tableRowsRel, nil)
	batch.Set(uid, str(TieTypeProperty), nil)
	_, err := tc.Batch(batch)
	return err
}

// appendClearTable appends ops that wipe an existing table's row entities so a
// re-import at the same uid leaves no orphaned rows. The table entity's own
// columns/rows/tie-type relations are overwritten by the caller's Set ops.
func (tc *TieClient) appendClearTable(batch *api.Batch, uid string) error {
	row, err := tc.Get(uid)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	oldRowUIDs := decodeOrderedList(RowValues(row, tableRowsRel))
	if len(oldRowUIDs) == 0 {
		return nil
	}
	oldRows, err := tc.Expand(oldRowUIDs)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	for _, r := range oldRows {
		for rel := range r.Attributes {
			batch.Set(r.Key, rel, nil)
		}
	}
	return nil
}
