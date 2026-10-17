package main

import (
	"strings"
	"testing"
)

// seedTriples adds a small, known set of triples for the argument-handling
// tests. Query terms are matched by reverse association and intersect on the
// {subject, relation} pair, so the values a query shares are deliberately all
// tags — that is the "things tagged with all of these" query the algebra means.
func seedTriples(t *testing.T, a *app) {
	t.Helper()
	for _, tr := range [][3]string{
		{"k1", "tag", "jazz"},
		{"k1", "tag", "cool"},
		{"k1", "artist", "miles"},
		{"k2", "tag", "jazz"},
		{"k2", "tag", "blue"},
		{"k3", "tag", "jazz"},
		{"k3", "tag", "cool"},
		{"k3", "tag", "blue"},
	} {
		if _, err := a.tie.Add(tr[0], tr[1], tr[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.tie.Sync(); err != nil {
		t.Fatal(err)
	}
}

// TestDelRefusesShellWildcards: `tie del key '*' '*'` was a trap — unquoted, the
// shell expands `*` to the directory's filenames before tie sees them, so the
// command deleted (key, file1, file2) or printed a hint and exited 0 (§6.7).
func TestDelRefusesShellWildcards(t *testing.T) {
	a := newTestApp(t)
	seedTriples(t, a)

	_, _, code, runErr := runTie(t, a, "", "del", "k1", "*", "*")
	if code != exitUsage {
		t.Errorf("`del k1 * *` exited %d, want %d", code, exitUsage)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "--all") {
		t.Errorf("the refusal does not point at the flags: %v", runErr)
	}

	stdout, _, _, _ := runTie(t, a, "", "get", "k1")
	if !strings.Contains(stdout, "jazz") {
		t.Errorf("the refused delete changed the store:\n%s", stdout)
	}
}

// TestDelByRelationAndAll: the two wildcard shapes become flags, and the command
// says how much it removed.
func TestDelByRelationAndAll(t *testing.T) {
	a := newTestApp(t)
	seedTriples(t, a)

	if _, _, code, runErr := runTie(t, a, "", "del", "k1", "--relation", "tag"); code != 0 {
		t.Fatalf("del --relation exited %d: %v", code, runErr)
	}
	stdout, _, _, _ := runTie(t, a, "", "get", "k1")
	if strings.Contains(stdout, "jazz") || !strings.Contains(stdout, "artist") {
		t.Errorf("--relation tag removed the wrong rows:\n%s", stdout)
	}

	if _, _, code, runErr := runTie(t, a, "", "del", "k2", "--all"); code != 0 {
		t.Fatalf("del --all exited %d: %v", code, runErr)
	}
	if stdout, _, _, _ := runTie(t, a, "", "get", "k2"); stdout != "" {
		t.Errorf("--all left rows:\n%s", stdout)
	}

	if _, _, code, _ := runTie(t, a, "", "del", "k1", "--all", "--relation", "tag"); code != exitUsage {
		t.Errorf("--all with --relation exited %d, want %d", code, exitUsage)
	}
	if _, _, code, _ := runTie(t, a, "", "del", "k1", "--all", "extra"); code != exitUsage {
		t.Errorf("--all with a second positional exited %d, want %d", code, exitUsage)
	}
}

// TestGetExclusionIsAFlag: `-b` collided with flag parsing, so exclusion needed
// `--`; --not and the ! spelling both work and agree (§6.7).
func TestGetExclusionIsAFlag(t *testing.T) {
	a := newTestApp(t)
	seedTriples(t, a)

	all, _, code, runErr := runTie(t, a, "", "get", "jazz", "cool")
	if code != 0 {
		t.Fatalf("get exited %d: %v", code, runErr)
	}
	if !strings.Contains(all, "k1") || !strings.Contains(all, "k3") {
		t.Fatalf("the unfiltered query is not what the seeds promise:\n%s", all)
	}

	flagged, _, _, _ := runTie(t, a, "", "get", "jazz", "cool", "--not", "blue")
	bang, _, _, _ := runTie(t, a, "", "get", "jazz", "cool", "!blue")

	if flagged != bang {
		t.Errorf("--not and ! disagree:\n%s\n---\n%s", flagged, bang)
	}
	if strings.Contains(flagged, "k3") || !strings.Contains(flagged, "k1") {
		t.Errorf("--not blue did not exclude k3:\n%s", flagged)
	}
}

// TestKeysFromStdin: `-` reads keys from stdin, one per line, so a pipeline
// needs no xargs and no quoting (§6.7).
func TestKeysFromStdin(t *testing.T) {
	a := newTestApp(t)
	seedTriples(t, a)

	// get - asks for each key's rows, not the intersection of all of them.
	stdout, _, code, runErr := runTie(t, a, "k1\nk2\n", "get", "-")
	if code != 0 {
		t.Fatalf("get - exited %d: %v", code, runErr)
	}
	for _, want := range []string{"k1\ttag\tjazz", "k2\ttag\tblue"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get - is missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "k3") {
		t.Errorf("get - included a key that was not on stdin:\n%s", stdout)
	}

	// --stdin is the spelling that survives arguments written after it: the
	// parser stops at a lone "-", so `get - --filter artist` loses the filter
	// while `get --stdin --filter artist` keeps it.
	stdout, _, code, runErr = runTie(t, a, "k1\nk2\n", "get", "--stdin", "--filter", "artist")
	if code != 0 {
		t.Fatalf("get --stdin exited %d: %v", code, runErr)
	}
	if stdout != "k1\tartist\tmiles\n" {
		t.Errorf("get --stdin --filter artist is wrong:\n%q", stdout)
	}

	// stat --stdin works through the same plumbing (the keys here are hashes,
	// which is what stat takes; both have to be read from the pipe).
	stdout, stderr, code, runErr := runTie(t, a,
		"aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000\n"+
			"bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000\n",
		"stat", "--stdin")
	if code != 0 {
		t.Fatalf("stat --stdin exited %d: %v", code, runErr)
	}
	if stdout != "" {
		t.Errorf("stat --stdin printed rows for hashes that do not exist:\n%s", stdout)
	}
	for _, want := range []string{"aaaa0000", "bbbb0000"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stat --stdin never reached %s:\n%s", want, stderr)
		}
	}
}

// TestTagSetStdin: the hashes come from the pipe and the tags stay arguments.
// The lone-dash form is refused, because the parser drops everything after `-`
// and a tag set with no tags clears them — a silent wipe is the one answer this
// command must not give (§6.7).
func TestTagSetStdin(t *testing.T) {
	a := newTestApp(t)
	seedTriples(t, a)

	_, _, code, runErr := runTie(t, a, "k1\nk2\n", "tag", "set", "-", "blue")
	if code != exitUsage {
		t.Errorf("`tag set - blue` exited %d, want %d: %v", code, exitUsage, runErr)
	}
	if stdout, _, _, _ := runTie(t, a, "", "get", "k1"); !strings.Contains(stdout, "jazz") {
		t.Errorf("the refused tag set cleared k1's tags:\n%s", stdout)
	}

	if _, _, code, runErr := runTie(t, a, "k1\nk2\n", "tag", "set", "--stdin", "blue"); code != 0 {
		t.Fatalf("tag set --stdin exited %d: %v", code, runErr)
	}
	for _, key := range []string{"k1", "k2"} {
		stdout, _, _, _ := runTie(t, a, "", "get", key)
		if !strings.Contains(stdout, "blue") || strings.Contains(stdout, "jazz") {
			t.Errorf("tag set --stdin did not replace the tags on %s:\n%s", key, stdout)
		}
	}
	if stdout, _, _, _ := runTie(t, a, "", "get", "k3"); !strings.Contains(stdout, "jazz") {
		t.Errorf("tag set --stdin touched a key that was not on stdin:\n%s", stdout)
	}
}

// TestDownloadFromStdinNeedsADirectory: several hashes land in DEST, so DEST has
// to be a directory — otherwise each download overwrites the last.
func TestDownloadFromStdinNeedsADirectory(t *testing.T) {
	a := newTestApp(t)
	_, _, code, runErr := runTie(t, a, "aaaa\nbbbb\n", "download", "-", "/no/such/directory")
	if code != exitUsage {
		t.Errorf("downloading two hashes to a missing directory exited %d, want %d: %v", code, exitUsage, runErr)
	}
}
