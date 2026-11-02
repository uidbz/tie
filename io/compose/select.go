package compose

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/uidbz/tie/metadata"
)

// selected is one entry of the composed input tree.
type selected struct {
	Name  string
	Entry metadata.DirEntry
	From  string // source label, for collision messages
}

// sourceTree indexes one already-walked output tree by path so selection needs
// no further fetches.
type sourceTree struct {
	root   *DirItem
	byPath map[string]*DirItem
}

func indexTree(root *DirItem) *sourceTree {
	src := &sourceTree{root: root, byPath: make(map[string]*DirItem)}
	var walk func(items []*DirItem)
	walk = func(items []*DirItem) {
		for _, it := range items {
			src.byPath[it.Path] = it
			if len(it.Children) > 0 {
				walk(it.Children)
			}
		}
	}
	walk(root.Children)
	return src
}

// selectEntries turns specs into the flat entry list of the composed tree. It is
// a pure function of already-fetched trees -- no network, no filesystem -- which
// is what makes the semantics unit-testable and lets the client and the runner
// share one implementation.
func selectEntries(roots map[string]*sourceTree, specs []Spec) ([]selected, error) {
	var out []selected
	placed := make(map[string]selected)

	add := func(name string, it *DirItem, spec Spec) error {
		if prev, ok := placed[name]; ok {
			// Identical content under one name is a harmless duplicate; differing
			// content silently loses a file, which is the bug .As() exists for.
			if prev.Entry.Hash == it.Hash {
				return nil
			}
			return fmt.Errorf("input name collision: %q selected from both %s and %s; use .As() to rename one",
				name, prev.From, spec.SourceLabel())
		}
		s := selected{
			Name:  name,
			Entry: metadata.DirEntry{Hash: it.Hash, Size: it.Size, Head: it.Head},
			From:  spec.SourceLabel(),
		}
		placed[name] = s
		out = append(out, s)
		return nil
	}

	for _, spec := range specs {
		src, ok := roots[spec.Hash]
		if !ok {
			if spec.Optional {
				continue
			}
			return nil, fmt.Errorf("input from %s: source tree unavailable", spec.SourceLabel())
		}
		if src.root.MediaType != "dir" {
			return nil, fmt.Errorf("input from %s: source is a file, not a directory", spec.SourceLabel())
		}

		switch {
		case spec.Glob != "":
			dir, pattern := path.Split(spec.Glob)
			dir = strings.Trim(dir, "/")
			children := src.root.Children
			if dir != "" {
				d, ok := src.byPath[dir]
				if !ok || d.MediaType != "dir" {
					if spec.Optional {
						continue
					}
					return nil, fmt.Errorf("input from %s: directory %q not found for glob %q",
						spec.SourceLabel(), dir, spec.Glob)
				}
				children = d.Children
			}
			matched := 0
			for _, c := range children {
				if c.MediaType != "file" {
					continue
				}
				ok, err := path.Match(pattern, c.Name)
				if err != nil {
					return nil, fmt.Errorf("input from %s: invalid glob %q: %w", spec.SourceLabel(), spec.Glob, err)
				}
				if !ok {
					continue
				}
				matched++
				if err := add(c.Name, c, spec); err != nil {
					return nil, err
				}
			}
			if matched == 0 && !spec.Optional {
				return nil, fmt.Errorf("input from %s: glob %q matched nothing", spec.SourceLabel(), spec.Glob)
			}

		case len(spec.Paths) > 0:
			for _, p := range spec.Paths {
				clean := strings.Trim(path.Clean(p), "/")
				it, ok := src.byPath[clean]
				if !ok {
					if spec.Optional {
						continue
					}
					return nil, fmt.Errorf("input from %s: %q not found", spec.SourceLabel(), p)
				}
				name := path.Base(clean)
				if spec.As != "" {
					name = spec.As
				}
				if err := add(name, it, spec); err != nil {
					return nil, err
				}
			}

		default:
			// Whole tree: splice the root's children, preserving nesting.
			for _, c := range src.root.Children {
				if err := add(c.Name, c, spec); err != nil {
					return nil, err
				}
			}
		}
	}

	return out, nil
}

// UnresolvedStepError reports a required spec whose step has no recorded output.
// It is a distinct type because the remedy differs by caller: a script is told to
// pre-seed a name, while a pipeline reports a step that never ran.
type UnresolvedStepError struct {
	Step string
}

func (e *UnresolvedStepError) Error() string {
	return fmt.Sprintf("input from step %q: no recorded output", e.Step)
}

// ResolveSteps fills in Hash for step-addressed specs by asking lookup for each
// step's recorded output. An optional spec whose step is unavailable is dropped;
// a required one is an UnresolvedStepError.
func ResolveSteps(specs []Spec, lookup func(step string) (string, bool)) ([]Spec, error) {
	out := make([]Spec, 0, len(specs))
	for _, s := range specs {
		if err := s.validate(); err != nil {
			return nil, err
		}
		if s.Hash == "" && s.FromStep != "" {
			hash, ok := lookup(s.FromStep)
			if !ok {
				if s.Optional {
					continue
				}
				return nil, &UnresolvedStepError{Step: s.FromStep}
			}
			s.Hash = hash
		}
		if s.Hash == "" {
			if s.Optional {
				continue
			}
			return nil, errors.New("input: source has neither a step nor a hash")
		}
		out = append(out, s)
	}
	return out, nil
}

// Dependency is one edge of a step graph, derived from the selection itself so
// dependencies can never drift out of sync with the inputs that create them.
type Dependency struct {
	Step     string
	Required bool // false when every spec naming this step is Optional
}

// Dependencies lists the distinct prior steps specs draw from, in first-seen
// order. A step is required unless every spec naming it is optional, which is
// what lets a pipeline advance past a branch that was deliberately skipped.
func Dependencies(specs []Spec) []Dependency {
	var out []Dependency
	at := make(map[string]int)
	for _, s := range specs {
		if s.FromStep == "" {
			continue
		}
		i, seen := at[s.FromStep]
		if !seen {
			at[s.FromStep] = len(out)
			out = append(out, Dependency{Step: s.FromStep, Required: !s.Optional})
			continue
		}
		if !s.Optional {
			out[i].Required = true
		}
	}
	return out
}

// Compose resolves specs into a single input tree and returns its root hash,
// which is the value to use as a job's InputHash. Every spec must already carry
// a Hash; call ResolveSteps first for step-addressed ones.
//
// Only directory manifests are fetched and only the synthesized manifests are
// written, so the cost is independent of how much data the tree references.
func Build(filehost, store string, specs []Spec) (string, error) {
	roots := make(map[string]*sourceTree)
	for _, s := range specs {
		// Validate at both entry points: the runner calls Compose directly,
		// without ResolveSteps (review B9k).
		if err := s.validate(); err != nil {
			return "", err
		}
		if s.Hash == "" {
			// Without a hash this would GET the filehost root and treat the
			// store listing as a tree. ResolveSteps rejects this upstream;
			// Compose is also called directly (by the runner), so guard here
			// with the identical message (review B9j).
			return "", errors.New("input: source has neither a step nor a hash")
		}
		if _, ok := roots[s.Hash]; ok {
			continue
		}
		items, err := DirTree(filehost, s.Hash, "")
		if err != nil {
			if s.Optional {
				// An optional source that cannot be walked yields nothing,
				// which is what makes selectEntries' Optional branch
				// reachable from Compose (review B9j).
				continue
			}
			return "", fmt.Errorf("input from %s: %w", s.SourceLabel(), err)
		}
		if len(items) != 1 {
			return "", fmt.Errorf("input from %s: unexpected tree root", s.SourceLabel())
		}
		roots[s.Hash] = indexTree(items[0])
	}

	entries, err := selectEntries(roots, specs)
	if err != nil {
		return "", err
	}

	builder := NewTreeBuilder()
	for _, e := range entries {
		if err := builder.Add(e.Name, e.Entry); err != nil {
			return "", err
		}
	}
	return builder.Upload(filehost, store)
}
