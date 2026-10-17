package main

import (
	"strings"
	"testing"
)

// TestValidateTSV pins the pre-flight pass that protects --drop: the whole
// input has to parse before the collection is destroyed, and the report has to
// say where it stops.
func TestValidateTSV(t *testing.T) {
	good := strings.NewReader("a\ttag\tx\nb\ttag\ty\nc\ttag\tz\n")
	n, err := validateTSV(good)
	if err != nil {
		t.Fatalf("a well-formed dump was rejected: %v", err)
	}
	if n != 3 {
		t.Errorf("validateTSV counted %d lines, want 3", n)
	}

	bad := strings.NewReader("a\ttag\tx\nb\ttag\ty\nbroken\tline\n")
	n, err = validateTSV(bad)
	if err == nil {
		t.Fatal("a two-field line was accepted")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the error does not name the line: %v", err)
	}
	if n != 3 {
		t.Errorf("it reported %d lines checked, want 3", n)
	}
}
