package client

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestSupersededChildren(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 7, d, 0, 0, 0, 0, time.UTC) }
	children := []childEntry{
		{Hash: "h1", Filename: "a.txt", TagDate: day(1)},    // unchanged: hash present
		{Hash: "h2old", Filename: "b.txt", TagDate: day(1)}, // content changed: old hash absent
		{Hash: "h3", Filename: "gone.txt", TagDate: day(1)}, // removed on disk: hash absent
	}
	// want is the set of hashes this import placed in the dir: h1 kept, b.txt
	// re-uploaded as h2new, gone.txt not present.
	want := map[string]bool{"h1": true, "h2new": true}
	got := supersededChildren(children, want)
	var names []string
	for _, c := range got {
		names = append(names, c.Filename)
	}
	sort.Strings(names)
	wantNames := []string{"b.txt", "gone.txt"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("supersededChildren = %v, want %v", names, wantNames)
	}
}

func TestRetentionDrops(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 7, d, 0, 0, 0, 0, time.UTC) }
	versions := []childEntry{
		{Hash: "newest", Filename: "f", TagDate: day(3)},
		{Hash: "oldest", Filename: "f", TagDate: day(1)},
		{Hash: "middle", Filename: "f", TagDate: day(2)},
	}

	// keep 0 or negative: handled by the no-history path, so no drops here.
	if got := retentionDrops(versions, 0); got != nil {
		t.Errorf("retentionDrops(keep=0) = %v, want nil", got)
	}
	// within cap: nothing dropped.
	if got := retentionDrops(versions, 3); got != nil {
		t.Errorf("retentionDrops(keep=3) = %v, want nil", got)
	}
	// keep newest 2 -> drop oldest.
	got := retentionDrops(versions, 2)
	if len(got) != 1 || got[0].Hash != "oldest" {
		t.Errorf("retentionDrops(keep=2) = %v, want [oldest]", got)
	}
	// keep 1 -> drop oldest then middle (oldest-first order).
	got = retentionDrops(versions, 1)
	if len(got) != 2 || got[0].Hash != "oldest" || got[1].Hash != "middle" {
		t.Errorf("retentionDrops(keep=1) = %v, want [oldest middle]", got)
	}
}

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
