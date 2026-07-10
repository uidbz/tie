package client

import (
	"reflect"
	"testing"
)

func TestTieDirAncestors(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"file:/a/b/c", []string{"file:/a", "file:/a/b", "file:/a/b/c"}},
		{"/a/b/c", []string{"file:/a", "file:/a/b", "file:/a/b/c"}},
		{"/a/b", []string{"file:/a", "file:/a/b"}},
		{"a/b", []string{"file:/a", "file:/a/b"}}, // relative treated as absolute
		{"file:/", nil},
		{"/", nil},
		{"file:/a//b/", []string{"file:/a", "file:/a/b"}},  // redundant separators
		{"file:/a/./b", []string{"file:/a", "file:/a/b"}},  // "." collapsed
		{"file:/a/x/../b", []string{"file:/a", "file:/a/b"}}, // ".." collapsed
	}
	for _, c := range cases {
		got := tieDirAncestors(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tieDirAncestors(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestStringToTieType(t *testing.T) {
	// Every TieType round-trips through its string form.
	for tt := TieUnknownFile; tt <= TieFile; tt++ {
		if got := StringToTieType(tt.String()); got != tt {
			t.Errorf("StringToTieType(%q) = %v, want %v", tt.String(), got, tt)
		}
	}
	// Unknown strings fall back to TieUnknownFile.
	if got := StringToTieType("not-a-type"); got != TieUnknownFile {
		t.Errorf("StringToTieType(unknown) = %v, want TieUnknownFile", got)
	}
}
