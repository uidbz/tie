package tiedb

import "testing"

// TestValueWithNULIsBinarySafe is the regression for §5.1 L2: getValueString
// used bytes.Trim(value, "\x00"), which stripped LEADING NULs (and a
// legitimate trailing one), so a value containing NUL bytes came back
// mangled — and two different values could collide onto the same string.
func TestValueWithNULIsBinarySafe(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "nul"})

	values := []string{
		"\x00leading",
		"\x00\x00double-leading",
		"mid\x00dle",
		"plain",
	}
	for _, v := range values {
		if err := col.Add("k", "rel", v); err != nil {
			t.Fatal(err)
		}
	}

	row, found := col.Get("k", "rel")
	if !found {
		t.Fatal("relation not found")
	}
	got := row["k"]["rel"]
	if len(got) != len(values) {
		t.Fatalf("stored %d distinct values, want %d (NUL trimming collided them): %q", len(got), len(values), valueKeys(got))
	}
	for _, v := range values {
		if _, ok := got[v]; !ok {
			t.Errorf("value %q came back mangled; got %q", v, valueKeys(got))
		}
	}
}

func valueKeys(v Value2) []string {
	var out []string
	for k := range v {
		out = append(out, k)
	}
	return out
}
