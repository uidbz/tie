package main

import "testing"

// TestRepairBlockedByIndex pins the §9 guard-rail: `tie verify --fix` (and
// --repair) must not run a destructive repair when the index check reported
// problems in the same run, because the tree scan it repairs against is seeded
// by reverse queries and can contain phantoms or miss children.
func TestRepairBlockedByIndex(t *testing.T) {
	cases := []struct {
		problems int
		repair   bool
		fix      bool
		dryRun   bool
		want     bool
		why      string
	}{
		{0, true, true, false, false, "a consistent index is no reason to refuse"},
		{3, false, false, false, false, "a plain read-only verify must still run"},
		{3, true, false, false, true, "--repair re-homes orphans from the scan"},
		{3, false, true, false, true, "--fix applies destructive mutations"},
		{3, true, true, false, true, "--fix implies --repair"},
		{3, false, true, true, false, "--fix --dry-run only prints a plan"},
	}
	for _, c := range cases {
		if got := repairBlockedByIndex(c.problems, c.repair, c.fix, c.dryRun); got != c.want {
			t.Errorf("problems=%d repair=%v fix=%v dry-run=%v: blocked=%v, want %v (%s)",
				c.problems, c.repair, c.fix, c.dryRun, got, c.want, c.why)
		}
	}
}
