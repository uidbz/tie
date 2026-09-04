package client

import (
	"errors"
	"testing"
)

// clearValueTypes removes the declarations a test made. The registry is one
// entity per collection and the client tests share a collection, so a test that
// left entries behind would leak into every other ValueTypes reader.
func clearValueTypes(t *testing.T, tie *TieClient, relations ...string) {
	t.Helper()
	cleanup := make(map[string]ValueType, len(relations))
	for _, r := range relations {
		cleanup[r] = ValueTypeString
	}
	if err := tie.SetValueTypes(cleanup); err != nil {
		t.Errorf("cleanup SetValueTypes: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Errorf("cleanup Sync: %v", err)
	}
}

func TestValueTypesRoundTrip(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	const (
		relMass = "vt-test-mass"
		relWhen = "vt-test-when"
	)
	defer clearValueTypes(t, tie, relMass, relWhen)

	if err := tie.SetValueTypes(map[string]ValueType{
		relMass: ValueTypeFloat,
		relWhen: ValueTypeDatetime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatal(err)
	}

	types, err := tie.ValueTypes()
	if err != nil {
		t.Fatal(err)
	}
	if types[relMass] != ValueTypeFloat {
		t.Errorf("ValueTypes()[%q] = %q, want %q", relMass, types[relMass], ValueTypeFloat)
	}
	if types[relWhen] != ValueTypeDatetime {
		t.Errorf("ValueTypes()[%q] = %q, want %q", relWhen, types[relWhen], ValueTypeDatetime)
	}
}

// A declaration replaces rather than accumulates: a relation holds one type, so
// re-declaring must not leave the old one in the set beside the new one.
func TestValueTypesRedeclareReplaces(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	const rel = "vt-test-redeclare"
	defer clearValueTypes(t, tie, rel)

	if err := tie.SetValueTypes(map[string]ValueType{rel: ValueTypeInt}); err != nil {
		t.Fatal(err)
	}
	if err := tie.SetValueTypes(map[string]ValueType{rel: ValueTypeFloat}); err != nil {
		t.Fatal(err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatal(err)
	}

	row, err := tie.Get(valueTypeRegistrySubject)
	if err != nil {
		t.Fatal(err)
	}
	if got := RowValues(row, rel); len(got) != 1 || got[0] != string(ValueTypeFloat) {
		t.Errorf("registry values for %q = %v, want [%s]", rel, got, ValueTypeFloat)
	}
}

// SetValueTypes merges: it must not disturb relations it was not given.
func TestValueTypesMergeAndClear(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	const (
		relKeep = "vt-test-keep"
		relDrop = "vt-test-drop"
	)
	defer clearValueTypes(t, tie, relKeep, relDrop)

	if err := tie.SetValueTypes(map[string]ValueType{
		relKeep: ValueTypeInt,
		relDrop: ValueTypeBool,
	}); err != nil {
		t.Fatal(err)
	}
	// Declaring string clears the entry, because an absent entry means string.
	if err := tie.SetValueTypes(map[string]ValueType{relDrop: ValueTypeString}); err != nil {
		t.Fatal(err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatal(err)
	}

	types, err := tie.ValueTypes()
	if err != nil {
		t.Fatal(err)
	}
	if types[relKeep] != ValueTypeInt {
		t.Errorf("ValueTypes()[%q] = %q, want %q (an unrelated declaration was disturbed)", relKeep, types[relKeep], ValueTypeInt)
	}
	if _, ok := types[relDrop]; ok {
		t.Errorf("ValueTypes() still reports %q as %q, want it cleared", relDrop, types[relDrop])
	}
}

// An unknown type in the registry reads as absent (i.e. string), so a collection
// written by a newer client stays readable instead of erroring.
func TestValueTypesUnknownTypeIgnoredOnRead(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	const rel = "vt-test-fromthefuture"
	defer func() {
		if err := tie.Set(valueTypeRegistrySubject, rel, nil); err != nil {
			t.Errorf("cleanup Set: %v", err)
		}
		if err := tie.Sync(); err != nil {
			t.Errorf("cleanup Sync: %v", err)
		}
	}()

	if err := tie.Set(valueTypeRegistrySubject, rel, []string{"complex128"}); err != nil {
		t.Fatal(err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatal(err)
	}

	types, err := tie.ValueTypes()
	if err != nil {
		t.Fatalf("ValueTypes() errored on an unknown type, want it ignored: %v", err)
	}
	if _, ok := types[rel]; ok {
		t.Errorf("ValueTypes() reported unknown type as %q, want it omitted", types[rel])
	}
}

func TestSetValueTypesRejectsUnknown(t *testing.T) {
	tie := NewTieClient(TestingConfig())

	err := tie.SetValueTypes(map[string]ValueType{"vt-test-bad": "decimal"})
	if !errors.Is(err, ErrUnknownValueType) {
		t.Errorf("SetValueTypes with a bogus type = %v, want ErrUnknownValueType", err)
	}
}

// One bad entry must declare nothing, so a caller can retry without having half
// its declarations already applied.
func TestSetValueTypesIsAllOrNothing(t *testing.T) {
	tie := NewTieClient(TestingConfig())
	requireServer(t, tie)

	const relGood = "vt-test-allornothing"
	defer clearValueTypes(t, tie, relGood)

	if err := tie.SetValueTypes(map[string]ValueType{
		relGood:           ValueTypeInt,
		"vt-test-alsobad": "decimal",
	}); err == nil {
		t.Fatal("SetValueTypes accepted a map containing an unknown type")
	}
	if err := tie.Sync(); err != nil {
		t.Fatal(err)
	}

	types, err := tie.ValueTypes()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := types[relGood]; ok {
		t.Errorf("ValueTypes()[%q] = %q, want nothing declared after a rejected batch", relGood, types[relGood])
	}
}

func TestValueTypesEmptyIsNoOp(t *testing.T) {
	tie := NewTieClient(TestingConfig())

	if err := tie.SetValueTypes(nil); err != nil {
		t.Errorf("SetValueTypes(nil) = %v, want nil", err)
	}
}
