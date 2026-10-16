package smbfs

import (
	"strings"
	"testing"
)

func TestEscapeName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"plain.txt", "plain.txt"},
		{"jazz mellow", "jazz mellow"}, // spaces are legal mid-name
		{"a/b", "a~2fb"},
		{`a\b`, "a~5cb"},
		{"type:audio", "type~3aaudio"},
		{"~x", "~7ex"},
		{"trail.", "trail~2e"},
		{"trail ", "trail~20"},
		{"...", "~2e~2e~2e"},
		{`*?"<>|`, "~2a~3f~22~3c~3e~7c"},
		{"ctl\x01char", "ctl~01char"},
		{"CON.txt", "~43ON.txt"},
		{"nul", "~6eul"},
		{"COM1.log", "~43OM1.log"},
		{"CONEY", "CONEY"}, // not a reserved base
		{"图片.jpg", "图片.jpg"}, // UTF-8 bytes pass through
	}
	for _, c := range cases {
		if got := escapeName(c.in); got != c.want {
			t.Errorf("escapeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEscapeRoundTrip(t *testing.T) {
	names := []string{
		"", "plain", "a/b", `a\b`, "type:audio", "~", "~7e", "~zz",
		"trail.", "trail ", "...", "a.b.", `*?"<>|`, "ctl\x01\x7f",
		"CON", "con.txt", "PRN7", "LPT1", "nul.txt",
		"jazz mellow -live", "type:audio", "图片 名.jpg",
		"\xff\xfe", "~~~", "~2e", "~20",
	}
	for _, n := range names {
		esc := escapeName(n)
		if got := unescapeName(esc); got != n {
			t.Errorf("round trip failed: %q -> %q -> %q", n, esc, got)
		}
	}
}

// TestEscapeInvariants checks the properties the smb transport depends on:
// escaped names never contain a byte the transport or Windows refuses, and
// the escape is collision-free (two different tie names never share one
// display name).
func TestEscapeInvariants(t *testing.T) {
	names := []string{
		"a/b", `a\b`, "a:b", `a*b`, "a?b", `a"b`, "a<b", "a>b", "a|b",
		"~", "~7e", "~2e", "trail.", "trail ", "CON", "con.txt", "...",
		"\x01", "\x7f", "~43ON", "~63on", "plain",
	}
	seen := make(map[string]string)
	for _, n := range names {
		esc := escapeName(n)
		if i := strings.IndexAny(esc, `\/*:"<>|?`); i >= 0 {
			t.Errorf("escapeName(%q) = %q still contains illegal byte %q", n, esc, esc[i])
		}
		if esc != "" && (esc[len(esc)-1] == '.' || esc[len(esc)-1] == ' ') {
			t.Errorf("escapeName(%q) = %q has an illegal trailing byte", n, esc)
		}
		if isReservedBase(esc) {
			t.Errorf("escapeName(%q) = %q is still a reserved name", n, esc)
		}
		for i := 0; i < len(esc); i++ {
			if esc[i] < 0x20 || esc[i] == 0x7f {
				t.Errorf("escapeName(%q) = %q still contains a control byte", n, esc)
			}
		}
		if other, dup := seen[esc]; dup && other != n {
			t.Errorf("escape collision: %q and %q both escape to %q", other, n, esc)
		}
		seen[esc] = n
	}
}

func TestUnescapeNameLiteralTilde(t *testing.T) {
	// A "~" not followed by two hex digits is kept verbatim: a client can
	// invent one, and it simply will not match any stored name.
	for _, s := range []string{"~", "~z", "~7", "a~b", "hello"} {
		if got := unescapeName(s); got != s {
			t.Errorf("unescapeName(%q) = %q, want unchanged", s, got)
		}
	}
	// Hex decoding is case-insensitive (clients may normalize).
	if got := unescapeName("~7E"); got != "~" {
		t.Errorf(`unescapeName("~7E") = %q, want "~"`, got)
	}
}
