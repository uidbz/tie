package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/internal/testenv"
)

// newTestApp starts the in-process servers and returns an app pointed at a
// fresh collection, so a test drives the same command tree the binary runs.
func newTestApp(t *testing.T) *app {
	t.Helper()
	env, stop, err := testenv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)

	cfg := client.Config{
		TripleStoreURL:   env.TripleStoreURL,
		Namespace:        "testing",
		Collection:       "testing",
		DefaultFileHosts: []string{"default"},
		FileHosts:        map[string]client.FileHost{"default": {URL: env.FileHostURL}},
	}
	return &app{config: cfg, tie: client.NewTieClientFor(cfg, "")}
}

// runTie runs the real command tree with args, feeding stdin from a string, and
// returns what the command wrote on each stream plus the exit status main would
// use. Files stand in for the streams so a chatty command cannot fill a pipe
// buffer and deadlock the run.
func runTie(t *testing.T, a *app, stdin string, args ...string) (stdout, stderr string, code int, runErr error) {
	t.Helper()

	dir := t.TempDir()
	inPath, outPath, errPath := filepath.Join(dir, "in"), filepath.Join(dir, "out"), filepath.Join(dir, "err")
	if err := os.WriteFile(inPath, []byte(stdin), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(inPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}

	savedIn, savedOut, savedErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, out, errFile
	runErr = newRootCommand(a).Run(context.Background(), append([]string{"tie"}, args...))
	os.Stdin, os.Stdout, os.Stderr = savedIn, savedOut, savedErr
	in.Close()
	out.Close()
	errFile.Close()

	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return read(outPath), read(errPath), exitCodeFor(runErr), runErr
}

// damagedCollection imports a small tree and adds a ghost file — a content hash
// that claims to be a file, has no metadata and no blob — which is the class
// `tie verify` reports and `tie repair` can fix by deleting.
func damagedCollection(t *testing.T, a *app) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.tie.ImportDir(dir, a.tie.Config.FileHosts["default"], "directory", nil, "/fsck", 0); err != nil {
		t.Fatal(err)
	}
	root, err := a.tie.DirUIDFromPath("tie:/fsck")
	if err != nil || root == "" {
		t.Fatalf("DirUIDFromPath: %q %v", root, err)
	}
	ghost := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := a.tie.Add(ghost, "parent", string(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.tie.Add(ghost, "tie-type", "file"); err != nil {
		t.Fatal(err)
	}
	if err := a.tie.Sync(); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyExitCodesFollowFsck: a script has to tell a damaged store from a
// broken network, and before §6.5 both were exit 1.
func TestVerifyExitCodesFollowFsck(t *testing.T) {
	t.Run("clean store is 0", func(t *testing.T) {
		a := newTestApp(t)
		stdout, _, code, _ := runTie(t, a, "", "verify")
		if code != 0 {
			t.Errorf("a clean store exited %d, want 0", code)
		}
		if stdout != "" {
			t.Errorf("a clean store printed findings: %q", stdout)
		}
	})

	t.Run("problems found is 1", func(t *testing.T) {
		a := newTestApp(t)
		damagedCollection(t, a)
		stdout, _, code, _ := runTie(t, a, "", "verify")
		if code != exitProblems {
			t.Errorf("a damaged store exited %d, want %d", code, exitProblems)
		}
		if !strings.Contains(stdout, "missing-metadata") {
			t.Errorf("the finding did not reach stdout: %q", stdout)
		}
	})

	t.Run("unreachable server is 4", func(t *testing.T) {
		cfg := client.Config{
			TripleStoreURL: "http://127.0.0.1:1",
			Namespace:      "testing",
			Collection:     "testing",
		}
		a := &app{config: cfg, tie: client.NewTieClientFor(cfg, "")}
		_, _, code, runErr := runTie(t, a, "", "verify")
		if code != exitOperational {
			t.Errorf("an unreachable store exited %d, want %d", code, exitOperational)
		}
		// main prints the message; the tree hands it up. A script sees the code,
		// an operator sees the reason.
		if runErr == nil || !strings.Contains(runErr.Error(), "index check") {
			t.Errorf("the failure was not explained: %v", runErr)
		}
	})

	t.Run("usage error is 2", func(t *testing.T) {
		a := newTestApp(t)
		_, _, code, _ := runTie(t, a, "", "verify", "--dry-run")
		if code != exitUsage {
			t.Errorf("--dry-run without --fix exited %d, want %d", code, exitUsage)
		}
	})
}

// TestFsckPipeline is the §6.5 contract: verify | repair plan | repair apply
// replaces --fix --dry-run --journal, each stage reading the previous one's
// stdout, and the store is clean afterwards.
func TestFsckPipeline(t *testing.T) {
	a := newTestApp(t)
	damagedCollection(t, a)

	findings, _, code, _ := runTie(t, a, "", "verify")
	if code != exitProblems {
		t.Fatalf("verify exited %d, want %d; output:\n%s", code, exitProblems, findings)
	}
	if lines := strings.Split(strings.TrimSpace(findings), "\n"); len(lines) == 0 {
		t.Fatal("verify printed no findings")
	}

	plan, planErr, code, _ := runTie(t, a, findings, "repair", "plan")
	if code != 0 {
		t.Fatalf("repair plan exited %d, want 0; stderr:\n%s", code, planErr)
	}
	ops := strings.Split(strings.TrimSpace(plan), "\n")
	if len(ops) == 0 || ops[0] == "" {
		t.Fatalf("repair plan produced no operations; stderr:\n%s", planErr)
	}
	for _, op := range ops {
		if fields := strings.Split(op, "\t"); len(fields) != 5 {
			t.Fatalf("plan record %q has %d fields, want 5", op, len(fields))
		}
	}

	journal, applyErr, code, _ := runTie(t, a, plan, "repair", "apply")
	if code != 0 {
		t.Fatalf("repair apply exited %d, want 0; stderr:\n%s", code, applyErr)
	}
	journalLines := strings.Split(strings.TrimSpace(journal), "\n")
	if len(journalLines) != len(ops) {
		t.Errorf("apply echoed %d journal records for %d operations", len(journalLines), len(ops))
	}
	for _, line := range journalLines {
		if fields := strings.Split(line, "\t"); len(fields) != 6 {
			t.Errorf("journal record %q has %d fields, want 6", line, len(fields))
		}
	}

	// The store is clean, and the journal replays as a plan.
	if _, _, code, _ := runTie(t, a, "", "verify"); code != 0 {
		t.Errorf("verify after the pipeline exited %d, want 0", code)
	}
	if _, err := client.ReadPlan(strings.NewReader(journal)); err != nil {
		t.Errorf("the journal is not a replayable plan: %v", err)
	}
}

// TestRepairPlanRejectsMalformedFindings: the input is usually a file someone
// edited, so a bad record is a usage error (2), not an operational failure.
func TestRepairPlanRejectsMalformedFindings(t *testing.T) {
	a := newTestApp(t)
	_, stderr, code, _ := runTie(t, a, "orphan-dir uid-1\n", "repair", "plan")
	if code != exitUsage {
		t.Errorf("malformed findings exited %d, want %d; stderr:\n%s", code, exitUsage, stderr)
	}
}

// TestRepairApplyRejectsAnUnrunnablePlan: an unknown operation or a record
// missing its key is refused before anything is applied.
func TestRepairApplyRejectsAnUnrunnablePlan(t *testing.T) {
	a := newTestApp(t)
	_, _, code, _ := runTie(t, a, "update\tk1\ttag\tjazz\tnote\n", "repair", "apply")
	if code != exitUsage {
		t.Errorf("an unknown operation exited %d, want %d", code, exitUsage)
	}
}

// TestVerifyReportIsPipedData: the report is the command's product, so it lands
// on stdout — the old hand-formatted version went to stderr and could not be
// piped at all.
func TestVerifyReportIsPipedData(t *testing.T) {
	a := newTestApp(t)
	damagedCollection(t, a)

	stdout, stderr, code, _ := runTie(t, a, "", "verify")
	if code == 0 {
		t.Fatal("expected findings")
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if fields := strings.Split(line, "\t"); len(fields) != 4 {
			t.Errorf("finding %q has %d fields, want 4", line, len(fields))
		}
	}
	if strings.Contains(stderr, "missing-metadata") {
		t.Errorf("the report is still on stderr:\n%s", stderr)
	}
}
