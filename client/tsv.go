package client

import (
	"encoding/csv"
	"fmt"
	"strings"
)

// The store's text format for triples (§6.4): one record per line, fields
// separated by a tab, and the PostgreSQL COPY escapes for the bytes that would
// otherwise break a line or shift a column — \t, \n, \r and \\.
//
// It is a wire format, not a CLI detail: `tie dump` writes it, `tie restore`
// reads it, and anything else that consumes a dump (a script, the Python
// client, an embedder) has to agree on it. encoding/csv with Comma='\t' looked
// like TSV but quoted any field containing a tab, a quote or a newline and
// doubled the quotes, so `cut`, `awk` and `sort` saw `"…"` artefacts while the
// Usage text promised plain TSV.

// EscapeTSVField writes one field with the COPY escapes applied.
func EscapeTSVField(s string) string {
	if !strings.ContainsAny(s, "\\\t\n\r") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// TSVRecord renders one record: fields joined by tabs, each escaped.
func TSVRecord(fields ...string) string {
	escaped := make([]string, len(fields))
	for i, f := range fields {
		escaped[i] = EscapeTSVField(f)
	}
	return strings.Join(escaped, "\t")
}

// unescapeTSVField decodes one escaped field. A backslash before anything else
// is a malformed record rather than a literal backslash, so a truncated dump is
// reported instead of quietly changing the value.
func unescapeTSVField(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("truncated escape at the end of %q", s)
		}
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		default:
			return "", fmt.Errorf("unknown escape \\%c in %q", s[i], s)
		}
	}
	return b.String(), nil
}

// SplitTSVRecord reads one record back. It splits on tabs — a tab inside a
// field is always escaped, so every literal tab is a separator — and unescapes.
//
// A line that does not split into want fields is retried in the quoted form
// `tie dump` wrote before §6.4, so dumps taken by older versions still
// restore. (A quoted field holding a real newline cannot be recovered line by
// line: re-export with the current `tie dump`.)
func SplitTSVRecord(line string, want int) ([]string, error) {
	fields := strings.Split(line, "\t")
	if len(fields) == want {
		out := make([]string, len(fields))
		for i, f := range fields {
			u, err := unescapeTSVField(f)
			if err != nil {
				return nil, err
			}
			out[i] = u
		}
		return out, nil
	}
	if !strings.Contains(line, `"`) {
		return nil, fmt.Errorf("got %d fields, want %d", len(fields), want)
	}
	legacy, err := readQuotedRecord(line, want)
	if err != nil {
		return nil, fmt.Errorf("got %d fields, want %d", len(fields), want)
	}
	return legacy, nil
}

// readQuotedRecord parses one record in the pre-§6.4 quoted form.
func readQuotedRecord(line string, want int) ([]string, error) {
	r := csv.NewReader(strings.NewReader(line))
	r.Comma = '\t'
	r.FieldsPerRecord = want
	rec, err := r.Read()
	if err != nil {
		return nil, err
	}
	return rec, nil
}
