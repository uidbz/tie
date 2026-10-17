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
	if len(row) != len(values) {
		t.Fatalf("stored %d distinct values, want %d (NUL trimming collided them): %q", len(row), len(values), row)
	}
	for _, v := range values {
		if !holdsValue(row, v) {
			t.Errorf("value %q came back mangled; got %q", v, row)
		}
	}
}

func holdsValue(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
