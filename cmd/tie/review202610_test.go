package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
)

// TestLateGlobalFlagsRejected pins #143: construction globals placed after
// the subcommand used to be parsed by cli but never applied, so `tie -f db
// add k tag v --read-only` exited 0 with the database written. They are now
// refused with a usage error. Spellings the subcommand defines as its own
// (get's -f filter, dump's --file, verify's --collection) stay legal.
func TestLateGlobalFlagsRejected(t *testing.T) {
	a, _ := localApp(t)

	// First write something, then probe the guard. Nothing written here
	// should matter, but it proves the commands work normally otherwise.
	if _, _, code, err := runTie(t, a, "", "add", "guard", "tag", "x"); code != 0 {
		t.Fatalf("add exited %d: %v", code, err)
	}

	late := []struct {
		name string
		args []string
	}{
		{"trailing --read-only", []string{"add", "k", "tag", "v", "--read-only"}},
		{"trailing -f", []string{"add", "k", "tag", "v", "-f", "other.tie"}},
		{"trailing -c", []string{"add", "k", "tag", "v", "-c", "Other"}},
		{"trailing --collection", []string{"get", "guard", "--collection", "Other"}},
		{"trailing -C", []string{"get", "guard", "-C", "/tmp/x.toml"}},
		{"trailing --reverse-relations", []string{"get", "guard", "--reverse-relations", "tag"}},
		{"trailing --read-only on get", []string{"get", "guard", "--read-only"}},
	}
	for _, tc := range late {
		_, _, code, runErr := runTie(t, a, "", tc.args...)
		if code != exitUsage {
			t.Errorf("%s: exited %d, want %d; err=%v", tc.name, code, exitUsage, runErr)
			continue
		}
		if runErr == nil || !strings.Contains(runErr.Error(), "global flag") {
			t.Errorf("%s: err = %v, want the global-flag usage error", tc.name, runErr)
		}
	}

	// `get --file x` collides with get's own -f alias, so cli itself refuses
	// the token before the guard sees it — rejected either way, never silent.
	if _, stderr, code, _ := runTie(t, a, "", "get", "guard", "--file", "other.tie"); code == 0 {
		t.Errorf("get --file other.tie exited 0, want a refusal; stderr=%q", stderr)
	}

	// Legal local spellings must NOT be swept up by the guard.
	legal := []struct {
		name string
		args []string
	}{
		{"get -f filter", []string{"get", "guard", "-f", "tag"}},
		{"verify --collection", []string{"verify", "--collection", "Main"}},
		{"trailing --format (applies via cli)", []string{"get", "guard", "--format", "tsv"}},
		// Commands that actually READ the inherited flag (inheritedReaders):
		// the placement applies, so the guard stays out.
		{"conf show -c", []string{"conf", "show", "-c", "Main"}},
		{"conf create -C", []string{"conf", "create", "-C", filepath.Join(t.TempDir(), "made.toml")}},
	}
	for _, tc := range legal {
		if _, stderr, code, _ := runTie(t, a, "", tc.args...); code == exitUsage && strings.Contains(stderr, "global flag") {
			t.Errorf("%s: rejected as a late global, must stay legal; stderr=%q", tc.name, stderr)
		}
	}
}

// TestGlobalScanPastPassThroughFlags pins #144: the leading scan used to
// stop at -q/-V/--format, dropping a later -f and silently opening the
// configured server instead of the -f file.
func TestGlobalScanPastPassThroughFlags(t *testing.T) {
	cases := []struct {
		args   []string
		file   string
		coll   string
		ro     bool
		config string
	}{
		{[]string{"tie", "-q", "-f", "db.tie", "get", "k"}, "db.tie", "", false, "config.toml"},
		{[]string{"tie", "-V", "--format", "json", "-f", "db.tie"}, "db.tie", "", false, "config.toml"},
		{[]string{"tie", "--format=table", "-f", "db.tie"}, "db.tie", "", false, "config.toml"},
		{[]string{"tie", "--quiet", "--read-only", "-f", "db.tie"}, "db.tie", "", true, "config.toml"},
		{[]string{"tie", "-q", "-f", "db.tie", "-c", "Other"}, "db.tie", "Other", false, "config.toml"},
		{[]string{"tie", "-C", "alt.toml", "-q", "-f", "db.tie"}, "db.tie", "", false, "alt.toml"},
	}
	for _, tc := range cases {
		flags := parseGlobalFlags(tc.args)
		if flags.dbFile != tc.file {
			t.Errorf("%q: dbFile = %q, want %q", tc.args, flags.dbFile, tc.file)
		}
		if flags.collection != tc.coll {
			t.Errorf("%q: collection = %q, want %q", tc.args, flags.collection, tc.coll)
		}
		if flags.readOnly != tc.ro {
			t.Errorf("%q: readOnly = %v, want %v", tc.args, flags.readOnly, tc.ro)
		}
		if flags.configName != tc.config {
			t.Errorf("%q: configName = %q, want %q", tc.args, flags.configName, tc.config)
		}
	}

	// A leading non-global is still the subcommand boundary: -c AFTER the
	// subcommand is not the global one (and is rejected by the guard).
	flags := parseGlobalFlags([]string{"tie", "verify", "-c", "Other"})
	if flags.collection != "" {
		t.Errorf("verify -c Other: global collection = %q, want untouched", flags.collection)
	}
}

// TestDumpOfflineFileRules pins #148: `dump --file` validates through the
// shared direct-open rules, `dump --file=` is a usage error instead of a nil
// client panic, and an unrelated broken config/client does not block an
// offline export.
func TestDumpOfflineFileRules(t *testing.T) {
	a, file := localApp(t)
	if _, _, code, err := runTie(t, a, "", "add", "dumpme", "tag", "v"); code != 0 {
		t.Fatalf("add exited %d: %v", code, err)
	}

	// --file= with an empty value: usage error, not server fall-through.
	_, stderr, code, _ := runTie(t, a, "", "dump", "--file=")
	if code != exitUsage {
		t.Errorf("dump --file= exited %d, want %d; stderr=%q", code, exitUsage, stderr)
	}

	// A directory named X.tie: rejected by the shared validator (#152).
	dir := t.TempDir()
	bad := filepath.Join(dir, "Bad.tie")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, code, _ = runTie(t, a, "", "dump", "--file", bad); code != exitOperational {
		t.Errorf("dump --file <directory.tie> exited %d, want %d", code, exitOperational)
	}

	// A broken client (pinned file missing) must not block a good offline
	// dump: the offline command is checked before the client error (#148).
	// Close the local app's client first: a plain (locked) offline dump
	// correctly refuses while a writer holds the file.
	if err := a.tie.Close(); err != nil {
		t.Fatal(err)
	}
	brokenCfg, err := client.ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	brokenCfg.TripleStoreFile = filepath.Join(filepath.Dir(file), "Gone.tie")
	broken := &app{config: brokenCfg, tie: client.NewTieClientFor(brokenCfg, "")}
	out, stderr, code, _ := runTie(t, broken, "", "dump", "--file", file)
	if code != 0 {
		t.Fatalf("offline dump with broken client exited %d: %s", code, stderr)
	}
	if !strings.Contains(out, "dumpme\ttag\tv") {
		t.Errorf("offline dump output = %q, want the dumped triple", out)
	}
}

// TestDumpFileReadOnlyBesideServer pins #148: `--read-only dump --file x`
// opens without the lock and runs beside a writer that holds the file, while
// a plain locked dump refuses (already covered by TestDumpFileRefusesServer);
// the exported triples come from disk.
func TestDumpFileReadOnlyBesideServer(t *testing.T) {
	a, file := localApp(t) // localApp keeps its own client open on the file
	if _, _, code, err := runTie(t, a, "", "add", "live", "tag", "v"); code != 0 {
		t.Fatalf("add exited %d: %v", code, err)
	}
	if err := a.tie.Sync(); err != nil {
		t.Fatal(err)
	}

	out, stderr, code, _ := runTie(t, a, "", "--read-only", "dump", "--file", file)
	if code != 0 {
		t.Fatalf("read-only dump beside the writer exited %d: %s", code, stderr)
	}
	if !strings.Contains(out, "live\ttag\tv") {
		t.Errorf("read-only dump output = %q, want the flushed triple", out)
	}

	// dump READS --read-only (inheritedReaders), so placing it after the
	// subcommand applies too: the export still opens without the lock.
	out, stderr, code, _ = runTie(t, a, "", "dump", "--file", file, "--read-only")
	if code != 0 {
		t.Fatalf("trailing --read-only dump exited %d: %s", code, stderr)
	}
	if !strings.Contains(out, "live\ttag\tv") {
		t.Errorf("trailing --read-only dump output = %q, want the flushed triple", out)
	}

	// The leading --read-only placement is required for commands that do NOT
	// read the flag (their trailing spelling is refused, covered in
	// TestLateGlobalFlagsRejected).
}
