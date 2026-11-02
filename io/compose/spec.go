// Package compose builds a new tie directory tree out of selections of
// existing trees, without moving any file content.
//
// Every hand-off is already content-addressed on the filehost and a tie
// directory is a small manifest listing its children by hash, so reshaping a
// tree set -- picking 4 files out of 600, renaming one, merging two trees --
// is an edit to a few kilobytes of manifest.
//
// The package is deliberately a leaf inside this module: that is what lets
// both a submitting client (which resolves step names against an in-process
// registry) and a sequencer (which resolves them against a job store)
// evaluate the identical vocabulary, so a multi-step run is written once
// rather than once per side.
package compose

import (
	"fmt"
	"strings"
)

// Spec selects part of an existing output tree to become part of a job's
// input. It names content rather than moving it.
//
// The vocabulary is shared between client and server: a client-side run
// evaluates these against Hash, while a server-side pipeline resolves FromStep
// against recorded step outputs and then applies the identical rules.
type Spec struct {
	FromStep string   // name of a prior step, resolved against a step registry
	Hash     string   // source tree, when addressed directly rather than by step
	Paths    []string // exact paths within the source tree
	Glob     string   // pattern matched against one directory's entries, e.g. "*.tsv"
	As       string   // rename the single selected file
	Optional bool     // yield nothing instead of erroring when absent
}

// SourceLabel names the spec's source for error messages, preferring the step
// name because that is what the caller wrote.
func (s Spec) SourceLabel() string {
	if s.FromStep != "" {
		return "step " + s.FromStep
	}
	if s.Hash == "" {
		return "unresolved source"
	}
	return "tree " + shortHash(s.Hash)
}

// Selection describes what the spec picks out, for error messages.
func (s Spec) Selection() string {
	switch {
	case s.Glob != "":
		return "glob " + s.Glob
	case len(s.Paths) > 0:
		return strings.Join(s.Paths, ", ")
	default:
		return "whole tree"
	}
}

// validate rejects specs whose combination of fields cannot be honoured. As
// renames exactly one file, so As with several Paths would rename every
// selection to the same name and surface as a confusing collision deep in
// selectEntries (review B9k). The Risor layer already blocks this at As();
// validating here covers specs built directly (runner pipelines, API callers).
func (s Spec) validate() error {
	if s.As != "" && len(s.Paths) > 1 {
		return fmt.Errorf("input from %s: As(%q) renames exactly one file, but %d paths are selected",
			s.SourceLabel(), s.As, len(s.Paths))
	}
	return nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
