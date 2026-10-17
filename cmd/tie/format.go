package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/uidbz/tie/client"

	"github.com/urfave/cli/v3"
)

// One text format for every listing command (§6.4).
//
// The format is tab-separated fields with the PostgreSQL COPY escapes: a
// literal tab, newline, carriage return or backslash in a field is written as
// \t, \n, \r or \\. That keeps one record per line, so cut, awk, sort and diff
// work, and it keeps a field from shifting the columns — which plain TSV cannot
// do and which encoding/csv "solved" by quoting, putting `"…"` artefacts in
// front of every consumer that was only trying to read a column.
//
// Every listing command writes through out, so the same three formats
// (tsv, json, table) behave the same way everywhere, and the row order is
// deterministic: the same query twice produces byte-identical output, which is
// what makes `diff`, `comm` and golden tests possible.

// The field codec itself lives with the store (client.TSVRecord,
// client.SplitTSVRecord): a dump is a file format other tools read, not a
// terminal detail. These are the local spellings the renderers use.

func tsvLine(fields ...string) string { return client.TSVRecord(fields...) }

// splitTSVLine reads one record back, in the escaped format or the quoted form
// `tie dump` wrote before §6.4.
func splitTSVLine(line string, want int) ([]string, error) {
	return client.SplitTSVRecord(line, want)
}

// sortRows orders records field by field, so two runs of one command agree byte
// for byte. The reports used to iterate maps, which made the same store print in
// a different order every run and broke diff, comm and golden tests (§6.4).
func sortRows(rows [][]string) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

// outputFormat resolves the global --format flag. An explicit choice wins;
// otherwise a terminal gets the aligned table and anything else — a pipe, a
// script, an embedder — gets TSV, which is what cut and sort consume (§6.4).
func outputFormat(ctx *cli.Command) (string, error) {
	switch f := ctx.String("format"); f {
	case "tsv", "json", "table":
		return f, nil
	case "":
		if term.IsTerminal(int(os.Stdout.Fd())) {
			return "table", nil
		}
		return "tsv", nil
	default:
		return "", fmt.Errorf("unknown --format %q: use tsv, json or table", f)
	}
}

// out renders the rows of one command in the format the user asked for. Rows
// are written as strings; json keys them by the column names the command
// declared, so `tie get x --format json` says what each column means.
type out struct {
	format string
	header []string
	w      io.Writer

	// table and json need the whole result: the table to size its columns,
	// the json to close its array.
	rows     [][]string
	jsonRows []map[string]string
	err      error
}

func newOut(format string, w io.Writer, header ...string) *out {
	return &out{format: format, header: header, w: w}
}

// row adds one record. A command that streams (dump) uses tsv/json directly
// and never calls flush with a table.
func (o *out) row(fields ...string) {
	switch o.format {
	case "tsv":
		if o.err == nil {
			_, o.err = fmt.Fprintln(o.w, tsvLine(fields...))
		}
	case "table":
		o.rows = append(o.rows, fields)
	case "json":
		obj := make(map[string]string, len(fields))
		for i, f := range fields {
			key := fmt.Sprintf("f%d", i+1)
			if i < len(o.header) {
				key = o.header[i]
			}
			obj[key] = f
		}
		o.jsonRows = append(o.jsonRows, obj)
	}
}

// flush writes what the buffered formats still owe.
func (o *out) flush() error {
	switch o.format {
	case "json":
		if o.jsonRows == nil {
			// An empty result is an empty array, not `null`: a consumer can
			// iterate it without a special case.
			o.jsonRows = []map[string]string{}
		}
		enc := json.NewEncoder(o.w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(o.jsonRows); err != nil {
			return err
		}
	case "table":
		if len(o.rows) == 0 {
			// No result, no table: a header on nothing would print a header
			// for every empty section of a multi-part report.
			return o.err
		}
		tw := tabwriter.NewWriter(o.w, 0, 2, 2, ' ', 0)
		if len(o.header) > 0 {
			hdr := make([]string, len(o.header))
			for i, h := range o.header {
				hdr[i] = strings.ToUpper(h)
			}
			fmt.Fprintln(tw, strings.Join(hdr, "\t"))
		}
		for _, r := range o.rows {
			// The table is the human format, but it still owes the reader one
			// line per record: a value carrying a tab or a newline is written
			// with the same escapes TSV uses, rather than shifting the columns
			// or splitting the row.
			escaped := make([]string, len(r))
			for i, f := range r {
				escaped[i] = client.EscapeTSVField(f)
			}
			fmt.Fprintln(tw, strings.Join(escaped, "\t"))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return o.err
}
