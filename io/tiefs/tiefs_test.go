package tiefs

import (
	"reflect"
	"testing"

	"github.com/uidbz/tie/client"
)

func TestParseTagQuery(t *testing.T) {
	tests := []struct {
		query     string
		wantScope string
		wantInc   []string
		wantExc   []string
	}{
		{"jazz", "", []string{"jazz"}, nil},
		{"jazz mellow -live", "", []string{"jazz", "mellow"}, []string{"live"}},
		{"mellow type:audio", "audio-file", []string{"mellow"}, nil},
		{"type:image", "image-file", nil, nil},
		{"  jazz   -live  ", "", []string{"jazz"}, []string{"live"}},
		{"type:live-album", "live-album", nil, nil},                 // custom label kept literally
		{"type:audio-dir jazz", "audio-dir", []string{"jazz"}, nil}, // full type name passes through
		{"-", "", nil, nil}, // bare dash ignored
	}
	for _, tt := range tests {
		gotScope, gotInc, gotExc := ParseTagQuery(tt.query)
		if gotScope != tt.wantScope {
			t.Errorf("ParseTagQuery(%q) scope = %q, want %q", tt.query, gotScope, tt.wantScope)
		}
		if !reflect.DeepEqual(gotInc, tt.wantInc) {
			t.Errorf("ParseTagQuery(%q) include = %v, want %q", tt.query, gotInc, tt.wantInc)
		}
		if !reflect.DeepEqual(gotExc, tt.wantExc) {
			t.Errorf("ParseTagQuery(%q) exclude = %v, want %q", tt.query, gotExc, tt.wantExc)
		}
	}
}

func TestDisambiguate(t *testing.T) {
	in := []client.TaggedFile{
		{Hash: "5075cc466d7a3f83aaaaaaaaaaaaaaaa", Filename: "SomeDir", IsDir: true},
		{Hash: "42e55dcfce802245bbbbbbbbbbbbbbbb", Filename: "SomeDir", IsDir: true},
		{Hash: "0f4098fbf4a52ae8cccccccccccccccc", Filename: "track1.jpg"},
		{Hash: "d39f1cac0b7accc6dddddddddddddddd", Filename: "track1.jpg"},
		{Hash: "eeee0000eeee0000eeee0000eeee0000", Filename: "unique.txt"},
	}
	got := Disambiguate(in)
	want := []string{
		"SomeDir",             // first keeps the bare name
		"SomeDir~42e55dcf",    // collision suffixed with short hash
		"track1.jpg",          // first keeps the bare name
		"track1~d39f1cac.jpg", // suffix inserted before the extension
		"unique.txt",          // no collision, untouched
	}
	for i, w := range want {
		if got[i].Filename != w {
			t.Errorf("entry %d: got %q, want %q", i, got[i].Filename, w)
		}
	}
	// Every resulting name must be unique so each match is reachable by Lookup.
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f.Filename] {
			t.Errorf("duplicate name after disambiguation: %q", f.Filename)
		}
		seen[f.Filename] = true
	}
}

func TestDisambiguateFiles(t *testing.T) {
	// A _prev history dir can hold several versions of one file, all recorded
	// under the same filename; DisambiguateFiles must uniquify them by hash.
	in := []client.File{
		{Uid: "0f4098fbf4a52ae8cccccccccccccccc", Filename: "file1.txt"},
		{Uid: "d39f1cac0b7accc6dddddddddddddddd", Filename: "file1.txt"},
		{Uid: "eeee0000eeee0000eeee0000eeee0000", Filename: "unique.txt"},
	}
	got := DisambiguateFiles(in)
	want := []string{
		"file1.txt",
		"file1~d39f1cac.txt",
		"unique.txt",
	}
	for i, w := range want {
		if got[i].Filename != w {
			t.Errorf("entry %d: got %q, want %q", i, got[i].Filename, w)
		}
	}
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f.Filename] {
			t.Errorf("duplicate name after disambiguation: %q", f.Filename)
		}
		seen[f.Filename] = true
	}
}

func TestBaseName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"tie:/music", "music"},
		{"tie:/music/jazz", "jazz"},
		{"tie:/music/jazz/", "jazz"},
		{"tie:/", ""},
		{"music", "music"},
	}
	for _, tt := range tests {
		if got := BaseName(tt.in); got != tt.want {
			t.Errorf("BaseName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestChildPath(t *testing.T) {
	if got := ChildPath("/", "music"); got != "/music" {
		t.Errorf("ChildPath(/, music) = %q", got)
	}
	if got := ChildPath("/music", "jazz"); got != "/music/jazz" {
		t.Errorf("ChildPath(/music, jazz) = %q", got)
	}
}

func TestParseLines(t *testing.T) {
	got := ParseLines([]byte(" jazz \n\n mellow\n"))
	want := []string{"jazz", "mellow"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLines = %v, want %v", got, want)
	}
	if len(ParseLines(nil)) != 0 {
		t.Error("ParseLines(nil) should be empty")
	}
}
