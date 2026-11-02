package main

import (
	"strings"
	"testing"
)

const sampleConfig = `Address = ':1163'
Filehost = 'https://runner1.example.com:1162'
Capabilities = ['CPUv1', 'GPUv1', 'CPUv1MaxCoreMaxMem']
Backends = ['https://runner1.example.com']
# operator note: keep specs fleet-wide identical
DefaultJobMemoryMB = 0
TotalCPU = 0

[JobTypeSpecs.CPUv1]
CPU = 8
RAMMB = 32768
GPU = 0

[[DeployedImages]]
Name = 'add5:latest'
Owner = 'ops@example.com'
DeployKey = 'secret123'
IsService = false

[[DeployedImages]]
Name = 'worker-agent:latest'
IsService = true
ServiceInternalPort = 8080
ServiceAutostart = false
`

func TestEditConfigUpdateAddDrop(t *testing.T) {
	sets := []SpecEdit{
		{Name: "CPUv1", CPU: 8, RAMMB: 8192, GPU: 0},
		{Name: "CPUv1Max64Core32GB", CPU: 64, RAMMB: 32768, GPU: 0},
	}
	out, changes, err := EditConfig(sampleConfig, sets, []string{"CPUv1MaxCoreMaxMem"}, "CPUv1Max64Core32GB")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[JobTypeSpecs.CPUv1]\nCPU = 8\nRAMMB = 8192\nGPU = 0") {
		t.Error("CPUv1 spec not updated in place")
	}
	if !strings.Contains(out, "[JobTypeSpecs.CPUv1Max64Core32GB]\nCPU = 64\nRAMMB = 32768\nGPU = 0") {
		t.Error("new spec not added")
	}
	if strings.Contains(out, "CPUv1MaxCoreMaxMem]") {
		t.Error("dropped spec section still present")
	}
	if strings.Contains(out, "'CPUv1MaxCoreMaxMem'") {
		t.Error("dropped capability still in Capabilities")
	}
	if !strings.Contains(out, "'CPUv1Max64Core32GB'") {
		t.Error("added capability missing")
	}
	// untouched content survives
	for _, keep := range []string{"DeployKey = 'secret123'", "# operator note", "ServiceAutostart = false", "Address = ':1163'"} {
		if !strings.Contains(out, keep) {
			t.Errorf("edit destroyed untouched content: %q", keep)
		}
	}
	// JobTypeSpecs must stay before [[DeployedImages]] (TOML table ordering)
	if strings.Index(out, "[JobTypeSpecs.CPUv1Max64Core32GB]") > strings.Index(out, "[[DeployedImages]]") {
		t.Error("new spec inserted after DeployedImages (invalid TOML table order)")
	}
	if len(changes) < 4 {
		t.Errorf("expected change reports, got %v", changes)
	}
}

func TestEditConfigIdempotent(t *testing.T) {
	sets := []SpecEdit{{Name: "CPUv1", CPU: 8, RAMMB: 8192, GPU: 0}}
	once, _, err := EditConfig(sampleConfig, sets, nil, "GPUv1")
	if err != nil {
		t.Fatal(err)
	}
	twice, changes, err := EditConfig(once, sets, nil, "GPUv1")
	if err != nil {
		t.Fatal(err)
	}
	if twice != once {
		t.Error("second application changed the text (not idempotent)")
	}
	if len(changes) != 2 {
		t.Errorf("expected 2 no-op reports, got %v", changes)
	}
	for _, c := range changes {
		if !strings.Contains(c, "already") {
			t.Errorf("expected only already-* reports, got %v", changes)
		}
	}
}

func TestEditConfigNoCapabilitiesLine(t *testing.T) {
	src := "Address = ':1163'\n"
	if _, _, err := EditConfig(src, nil, nil, "CPUv1"); err == nil {
		t.Error("expected error when Capabilities line is absent")
	}
}

func TestEditConfigDropLeavesCleanArray(t *testing.T) {
	// drop the only capability -> empty array, no stray commas
	out, _, err := EditConfig("Capabilities = ['CPUv1']\n", nil, []string{"CPUv1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Capabilities = []") {
		t.Errorf("bad empty array: %q", out)
	}
	// drop the middle of three
	out, _, err = EditConfig("Capabilities = ['a', 'b', 'c']\n", nil, []string{"b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Capabilities = ['a', 'c']") {
		t.Errorf("bad middle removal: %q", out)
	}
}

func TestEditConfigNoSpecsSectionAtAll(t *testing.T) {
	src := "Capabilities = ['CPUv1']\n[[DeployedImages]]\nName = 'x:latest'\n"
	out, _, err := EditConfig(src, []SpecEdit{{Name: "Big", CPU: 64, RAMMB: 1, GPU: 0}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(out, "[JobTypeSpecs]") > strings.Index(out, "[[DeployedImages]]") {
		t.Error("created [JobTypeSpecs] after DeployedImages")
	}
}

func TestSetAutostart(t *testing.T) {
	out, report, missing := SetAutostart(sampleConfig, []string{"worker-agent:latest", "ghost:latest"})
	if len(missing) != 1 || missing[0] != "ghost:latest" {
		t.Errorf("missing report wrong: %v", missing)
	}
	if !strings.Contains(out, "ServiceAutostart = true") {
		t.Error("autostart not set")
	}
	if strings.Contains(out, "ServiceAutostart = false") {
		t.Error("old false value still present")
	}
	if len(report) != 1 || !strings.Contains(report[0], "false -> true") {
		t.Errorf("report wrong: %v", report)
	}
	// idempotent second pass
	out2, report2, _ := SetAutostart(out, []string{"worker-agent:latest"})
	if out2 != out {
		t.Error("not idempotent")
	}
	if len(report2) != 1 || !strings.Contains(report2[0], "already true") {
		t.Errorf("second-pass report wrong: %v", report2)
	}
}

func TestSetAutostartAddsMissingField(t *testing.T) {
	src := "[[DeployedImages]]\nName = 'svc:latest'\nIsService = true\n"
	out, report, missing := SetAutostart(src, []string{"svc:latest"})
	if len(missing) != 0 {
		t.Fatalf("unexpected missing: %v", missing)
	}
	if !strings.Contains(out, "ServiceAutostart = true") {
		t.Errorf("field not appended: %q", out)
	}
	if len(report) != 1 || !strings.Contains(report[0], "field added") {
		t.Errorf("report wrong: %v", report)
	}
}

func TestNoAutostart(t *testing.T) {
	names := NoAutostart(sampleConfig)
	if len(names) != 1 || names[0] != "worker-agent:latest" {
		t.Errorf("NoAutostart wrong: %v", names)
	}
}

func TestParseSpec(t *testing.T) {
	e, err := parseSpec("CPUv1HighCore:64:32768:0")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "CPUv1HighCore" || e.CPU != 64 || e.RAMMB != 32768 || e.GPU != 0 {
		t.Errorf("parse wrong: %+v", e)
	}
	if _, err := parseSpec("bad:1"); err == nil {
		t.Error("expected error for malformed spec")
	}
}
