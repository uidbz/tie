package client

import (
	"errors"
	"fmt"
)

// Value types are an optional, advisory annotation on a *relation*: a note that
// every value stored under it is meant to be read as a number, a date, and so
// on. The store itself never validates a value against its relation's declared
// type — values remain plain strings on the wire and on disk — so this is a hint
// for readers, not a constraint on writers. A relation with no declaration is a
// string, which is what every relation written before this existed is.
//
// The declarations live in one registry entity per collection, whose attributes
// *are* the schema:
//
//	("value-types", <relation>, <type>)
//
// A single forward Get therefore returns the whole collection's schema, already
// shaped as relation -> type by Row.Attributes, with no reverse-index dependency
// and no per-value or per-row cost: one triple per declared relation.
//
// Typing relations rather than values is what makes this cheap enough to be
// worth having. It also types table columns for free: a table stores each cell
// as (rowEntity, <column key>, <cell>), so a column's key *is* a relation and
// declaring its type says how to read every cell in that column (see table.go).
// The trade-off is that a relation has one type per collection — two tables in
// one collection cannot disagree about what "mass" holds.
const valueTypeRegistrySubject = "value-types"

// ValueType is a declared reading of the values under a relation. The set is
// closed; readers treat anything else as ValueTypeString so a collection written
// by a newer client stays readable.
type ValueType string

const (
	// ValueTypeString is the default and need not be declared.
	ValueTypeString ValueType = "string"
	// ValueTypeInt is an optionally signed run of decimal digits (strconv.ParseInt).
	ValueTypeInt ValueType = "int"
	// ValueTypeFloat is a decimal number with "." as the point and no thousands
	// separators (strconv.ParseFloat, 64-bit).
	ValueTypeFloat ValueType = "float"
	// ValueTypeBool is "true" or "false", lower case.
	ValueTypeBool ValueType = "bool"
	// ValueTypeDate is YYYY-MM-DD.
	ValueTypeDate ValueType = "date"
	// ValueTypeDatetime is RFC3339.
	ValueTypeDatetime ValueType = "datetime"
)

// ErrUnknownValueType is returned by SetValueTypes when asked to declare a type
// outside the closed vocabulary.
var ErrUnknownValueType = errors.New("client: unknown value type")

// Valid reports whether t is one of the declared types. ValueTypeString counts:
// it is a legal declaration even though it is also the default.
func (t ValueType) Valid() bool {
	switch t {
	case ValueTypeString, ValueTypeInt, ValueTypeFloat, ValueTypeBool, ValueTypeDate, ValueTypeDatetime:
		return true
	}
	return false
}

// ValueTypes returns every value type declared in the collection, keyed by
// relation. A collection with no declarations yields an empty map and a nil
// error, so callers need not distinguish "no registry" from "nothing declared".
// Relations declared with a type this client does not know are omitted rather
// than reported: an unknown type reads as a string, which is the default anyway.
//
// This is one round trip for a whole collection's schema; fetch it once per
// collection and cache it rather than calling it per table or per row.
func (tc *TieClient) ValueTypes() (map[string]ValueType, error) {
	row, err := tc.Get(valueTypeRegistrySubject)
	if errors.Is(err, ErrNotFound) {
		return map[string]ValueType{}, nil
	}
	if err != nil {
		return nil, err
	}
	types := make(map[string]ValueType, len(row.Attributes))
	for relation := range row.Attributes {
		t := ValueType(RowFirst(row, relation))
		if !t.Valid() {
			continue
		}
		types[relation] = t
	}
	return types, nil
}

// SetValueTypes declares the value type of each named relation, merging into
// whatever the collection already holds: relations not named are left alone.
// Declaring ValueTypeString (or the empty string) clears a relation's entry,
// since an absent declaration already means string.
//
// Every type is checked before anything is written, so a map containing one bad
// entry declares nothing. Declarations replace rather than accumulate — a
// relation carries one type, not a set of them.
func (tc *TieClient) SetValueTypes(types map[string]ValueType) error {
	if len(types) == 0 {
		return nil
	}
	for relation, t := range types {
		if relation == "" {
			return fmt.Errorf("client: cannot declare a value type for the empty relation")
		}
		if t != "" && !t.Valid() {
			return fmt.Errorf("%w %q for relation %q", ErrUnknownValueType, t, relation)
		}
	}

	batch := tc.NewBatch()
	for relation, t := range types {
		if t == "" || t == ValueTypeString {
			batch.Set(valueTypeRegistrySubject, relation, nil)
			continue
		}
		batch.Set(valueTypeRegistrySubject, relation, []string{string(t)})
	}
	_, err := tc.Batch(batch)
	return err
}
