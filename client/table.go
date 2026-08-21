package client

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"git.sr.ht/~uid/tie/api"
)

// A table is a client-side convenience over the triple store: a rectangular
// grid of string cells with ordered column headers, built entirely on the
// existing Query/Set/Get/Expand/Batch primitives. There is no server-side table
// type — the daemon still sees only triples.
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
// The store returns a subject's multi-value attributes sorted, not in insertion
// order, so each entry in the columns/rows lists is prefixed with a zero-padded
// ordinal: the server's lexicographic sort then reproduces the original order.
// Reads are a forward Get(T) + Expand(rowUIDs) — no reverse-index dependency and
// no daemon config. The trade-off is that "rows" is a single multi-value
// attribute, so this targets sheet-sized tables (hundreds to low thousands of
// rows), not very large ones. Headers must be unique and none may be named
// "tie-type" (it would collide with a row entity's type marker); either case is
// rejected with an error.
const (
	tableColumnsRel = "columns"
	tableRowsRel    = "rows"
	tieTypeTable    = "table"
	tieTypeTableRow = "table-row"

	// orderSep separates the ordinal prefix from the payload in a list entry.
	// NUL is used so it will not appear in a header or a hex row uid.
	orderSep = "\x00"
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

// InsertTable writes headers + rows as a table entity and returns its uid. An
// empty uid mints a fresh one; a non-empty uid replaces the table already stored
// there (idempotent re-import) — its old row entities are cleared first. rows
// are row-major; each inner slice holds one row's cells in header order. Short
// rows are padded with empty cells and cells past len(headers) are ignored.
// Empty cells are not stored (ReadTable reconstructs them as "").
func (tc *TieClient) InsertTable(uid string, headers []string, rows [][]string) (string, error) {
	seen := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		if h == str(TieTypeProperty) {
			return "", fmt.Errorf("client: column name %q is reserved and cannot be used as a table header", h)
		}
		if _, dup := seen[h]; dup {
			return "", fmt.Errorf("client: duplicate column name %q; table headers must be unique", h)
		}
		seen[h] = struct{}{}
	}

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

	columns := make([]string, len(headers))
	for j, header := range headers {
		columns[j] = encodeOrdered(j, header)
	}
	batch.Set(uid, str(TieTypeProperty), []string{tieTypeTable})
	batch.Set(uid, tableColumnsRel, columns)

	rowRefs := make([]string, len(rows))
	for i, cells := range rows {
		rowUID := string(tc.newDirUID())
		rowRefs[i] = encodeOrdered(i, rowUID)
		batch.Set(rowUID, str(TieTypeProperty), []string{tieTypeTableRow})
		for j, header := range headers {
			if j < len(cells) && cells[j] != "" {
				batch.Add(rowUID, header, cells[j])
			}
		}
	}
	batch.Set(uid, tableRowsRel, rowRefs)

	if _, err := tc.Batch(batch); err != nil {
		return "", err
	}
	return uid, nil
}

// ReadTable returns a table's headers and rows (row-major, header order),
// or ErrNotFound if uid holds no table entity.
func (tc *TieClient) ReadTable(uid string) (headers []string, rows [][]string, err error) {
	row, err := tc.Get(uid)
	if err != nil {
		return nil, nil, err
	}
	headers = decodeOrderedList(RowValues(row, tableColumnsRel))
	rowUIDs := decodeOrderedList(RowValues(row, tableRowsRel))
	if len(rowUIDs) == 0 {
		return headers, nil, nil
	}

	expanded, err := tc.Expand(rowUIDs)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, nil, err
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
		cells := make([]string, len(headers))
		for j, header := range headers {
			cells[j] = RowFirst(r, header)
		}
		rows[i] = cells
	}
	return headers, rows, nil
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
