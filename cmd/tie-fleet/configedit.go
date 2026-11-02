// Textual edits of a runner config TOML. Deliberately NOT a TOML
// round-trip: the runner rewrites this file itself (deploys append
// [[DeployedImages]]), and a round-trip would eat the operator's comments and
// reformat secrets. These edits preserve everything they do not touch, are
// idempotent, and are unit-tested (configedit_test.go).
package main

import (
	"fmt"
	"regexp"
	"strings"
)

// SpecEdit is one [JobTypeSpecs.<name>] add/update.
type SpecEdit struct {
	Name  string
	CPU   int
	RAMMB int
	GPU   int
}

// EditConfig applies spec edits, spec drops, and a capability addition to the
// config text. It reports what it changed; unchanged input returns changed=false.
func EditConfig(src string, sets []SpecEdit, drops []string, addCap string) (out string, changes []string, err error) {
	out = src
	for _, s := range sets {
		if s.Name == "" || s.CPU <= 0 {
			return "", nil, fmt.Errorf("bad spec edit for %q: CPU must be > 0", s.Name)
		}
		out, changes = upsertSpecSection(out, s, changes)
	}
	for _, name := range drops {
		out, changes = dropSpecSection(out, name, changes)
		out, changes = removeCapability(out, name, changes)
	}
	if addCap != "" {
		out, changes, err = addCapability(out, addCap, changes)
		if err != nil {
			return "", nil, err
		}
	}
	return out, changes, nil
}

var (
	reCapabilities = regexp.MustCompile(`(?m)^Capabilities = \[([^\]]*)\]`)
)

// specSectionRange finds the [JobTypeSpecs.<name>] section: the header line
// through the line before the next table header (or EOF). Go's RE2 has no
// lookahead, so this is a line scan rather than one regex.
func specSectionRange(lines []string, name string) (start, end int, found bool) {
	hdr := "[JobTypeSpecs." + name + "]"
	for i, ln := range lines {
		if strings.TrimSpace(ln) != hdr {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
				return i, j, true
			}
		}
		return i, len(lines), true
	}
	return 0, 0, false
}

func specBlock(s SpecEdit) string {
	return fmt.Sprintf("[JobTypeSpecs.%s]\nCPU = %d\nRAMMB = %d\nGPU = %d\n", s.Name, s.CPU, s.RAMMB, s.GPU)
}

func upsertSpecSection(src string, s SpecEdit, changes []string) (string, []string) {
	lines := strings.Split(src, "\n")
	if i, end, ok := specSectionRange(lines, s.Name); ok {
		repl := strings.Split(strings.TrimRight(specBlock(s), "\n"), "\n")
		out := append(lines[:i], append(repl, lines[end:]...)...)
		joined := strings.Join(out, "\n")
		if joined == src {
			return src, append(changes, s.Name+": spec already current")
		}
		return joined, append(changes, s.Name+": spec updated")
	}
	block := strings.Split(strings.TrimRight(specBlock(s), "\n"), "\n")
	// Insert after an existing [JobTypeSpecs] header line, else create the
	// table group before the first [[DeployedImages]], else append at end.
	// Keeping JobTypeSpecs above DeployedImages matters: TOML bare tables
	// cannot be re-opened after array-of-tables entries begin.
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "[JobTypeSpecs]" {
			lines = append(lines[:i+1], append(block, lines[i+1:]...)...)
			return strings.Join(lines, "\n"), append(changes, s.Name+": spec added")
		}
	}
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "[[DeployedImages]]" {
			ins := append([]string{"[JobTypeSpecs]"}, block...)
			lines = append(lines[:i], append(ins, lines[i:]...)...)
			return strings.Join(lines, "\n"), append(changes, s.Name+": spec added")
		}
	}
	trimmed := strings.TrimRight(src, "\n")
	return trimmed + "\n\n[JobTypeSpecs]\n" + specBlock(s), append(changes, s.Name+": spec added")
}

func dropSpecSection(src, name string, changes []string) (string, []string) {
	lines := strings.Split(src, "\n")
	if i, end, ok := specSectionRange(lines, name); ok {
		lines = append(lines[:i], lines[end:]...)
		return strings.Join(lines, "\n"), append(changes, name+": spec section removed")
	}
	return src, changes
}

// capQuoted matches 'name' or "name" inside the Capabilities array.
func capQuoted(name string) *regexp.Regexp {
	return regexp.MustCompile(`'` + regexp.QuoteMeta(name) + `'|"` + regexp.QuoteMeta(name) + `"`)
}

func removeCapability(src, name string, changes []string) (string, []string) {
	m := reCapabilities.FindStringSubmatchIndex(src)
	if m == nil {
		return src, changes
	}
	inner := src[m[2]:m[3]]
	if !capQuoted(name).MatchString(inner) {
		return src, changes
	}
	cleaned := capQuoted(name).ReplaceAllString(inner, "")
	// Collapse leftover separators: ", ,", leading/trailing commas.
	cleaned = regexp.MustCompile(`,\s*,`).ReplaceAllString(cleaned, ",")
	cleaned = strings.Trim(strings.TrimSpace(cleaned), ",")
	cleaned = regexp.MustCompile(`,\s*$`).ReplaceAllString(cleaned, "")
	return src[:m[2]] + cleaned + src[m[3]:], append(changes, name+": removed from Capabilities")
}

func addCapability(src, name string, changes []string) (string, []string, error) {
	m := reCapabilities.FindStringSubmatchIndex(src)
	if m == nil {
		return "", nil, fmt.Errorf("no Capabilities line found in config")
	}
	inner := src[m[2]:m[3]]
	if capQuoted(name).MatchString(inner) {
		return src, append(changes, "Capabilities: "+name+" already present"), nil
	}
	sep := ", "
	if strings.TrimSpace(inner) == "" {
		sep = ""
	}
	return src[:m[3]] + sep + "'" + name + "'" + src[m[3]:], append(changes, "Capabilities: + "+name), nil
}

// SplitDeployedBlocks splits config text at [[DeployedImages]] boundaries
// (line scan: Go's RE2 has no lookahead).
func SplitDeployedBlocks(src string) []string {
	lines := strings.Split(src, "\n")
	var blocks []string
	start := 0
	for i, ln := range lines {
		if i > 0 && strings.TrimSpace(ln) == "[[DeployedImages]]" {
			blocks = append(blocks, strings.Join(lines[start:i], "\n"))
			start = i
		}
	}
	return append(blocks, strings.Join(lines[start:], "\n"))
}

var reImageName = regexp.MustCompile(`(?m)^Name = '([^']+)'`)

// SetAutostart sets ServiceAutostart=true in the [[DeployedImages]] blocks
// whose Name is in names. Returns the new text, per-service report lines, and
// the requested names that were not found.
func SetAutostart(src string, names []string) (out string, report []string, missing []string) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	blocks := SplitDeployedBlocks(src)
	for i, block := range blocks {
		m := reImageName.FindStringSubmatch(block)
		if m == nil || !want[m[1]] {
			continue
		}
		name := m[1]
		delete(want, name)
		switch {
		case regexp.MustCompile(`(?m)^ServiceAutostart = true`).MatchString(block):
			report = append(report, name+": already true")
		case regexp.MustCompile(`(?m)^ServiceAutostart = false`).MatchString(block):
			blocks[i] = regexp.MustCompile(`(?m)^ServiceAutostart = false`).ReplaceAllString(block, "ServiceAutostart = true")
			report = append(report, name+": false -> true")
		default:
			blocks[i] = strings.TrimRight(block, "\n") + "\nServiceAutostart = true\n"
			report = append(report, name+": field added = true")
		}
	}
	for n := range want {
		missing = append(missing, n)
	}
	return strings.Join(blocks, "\n"), report, missing
}

// NoAutostart lists service names whose block has ServiceAutostart = false.
func NoAutostart(src string) []string {
	var names []string
	for _, block := range SplitDeployedBlocks(src) {
		m := reImageName.FindStringSubmatch(block)
		if m != nil && regexp.MustCompile(`(?m)^ServiceAutostart = false`).MatchString(block) {
			names = append(names, m[1])
		}
	}
	return names
}
