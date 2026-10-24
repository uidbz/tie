package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/internal/testenv"
)

// localApp builds the CLI app the way main does for `tie -f file`.
func localApp(t *testing.T) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "Main.tie")
	if err := os.WriteFile(file, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg, err := client.ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	tie := client.NewTieClientFor(cfg, "")
	t.Cleanup(func() { _ = tie.Close() })
	return &app{config: cfg, tie: tie}, file
}

// TestFileModeCommands runs the real command tree against a direct database and
// checks the reporting surfaces (get, dump, conf show, version).
func TestFileModeCommands(t *testing.T) {
	a, file := localApp(t)

	if _, stderr, code, err := runTie(t, a, "", "add", "abc", "tag", "jazz"); code != 0 {
		t.Fatalf("add exited %d: %v (%s)", code, err, stderr)
	}

	out, _, code, err := runTie(t, a, "", "get", "-r", "jazz")
	if code != 0 {
		t.Fatalf("get exited %d: %v", code, err)
	}
	if !strings.Contains(out, "abc\ttag\tjazz") {
		t.Errorf("get output = %q", out)
	}

	out, _, code, err = runTie(t, a, "", "dump")
	if code != 0 {
		t.Fatalf("dump exited %d: %v", code, err)
	}
	if !strings.Contains(out, "abc\ttag\tjazz") {
		t.Errorf("dump output = %q", out)
	}

	out, _, code, err = runTie(t, a, "", "conf", "show")
	if code != 0 {
		t.Fatalf("conf show exited %d: %v", code, err)
	}
	if !strings.Contains(out, "db-file") || !strings.Contains(out, "file:"+file) {
		t.Errorf("conf show output = %q", out)
	}
	if !strings.Contains(out, "read-only") || !strings.Contains(out, "reverse-relations") {
		t.Errorf("conf show does not report the direct-mode knobs: %q", out)
	}

	out, _, code, err = runTie(t, a, "", "version")
	if code != 0 {
		t.Fatalf("version exited %d: %v", code, err)
	}
	if !strings.Contains(out, "triplestore (local file)") || !strings.Contains(out, file) {
		t.Errorf("version output = %q", out)
	}
	if strings.Contains(out, "unavailable") {
		t.Errorf("version probed a filehost/URL in offline mode: %q", out)
	}
}

// TestDumpFileWithoutConfig: the subcommand's offline export must not require a
// config, even though the global -f path does not apply.
func TestDumpFileWithoutConfig(t *testing.T) {
	a, file := localApp(t)
	if _, _, code, err := runTie(t, a, "", "add", "abc", "tag", "jazz"); code != 0 {
		t.Fatalf("add exited %d: %v", code, err)
	}
	if err := a.tie.Close(); err != nil {
		t.Fatal(err)
	}

	// A bare app with no config or client: dump --file is the only path that
	// may run.
	out, stderr, code, err := runTie(t, &app{}, "", "dump", "--file", file)
	if code != 0 {
		t.Fatalf("dump --file exited %d: %v (%s)", code, err, stderr)
	}
	if !strings.Contains(out, "abc\ttag\tjazz") {
		t.Errorf("dump --file output = %q", out)
	}
}

// TestParseGlobalFileFlag pins the leading-global scan: -f is the database file
// before the command, but get's -f filter after it stays a subcommand flag.
func TestParseGlobalFileFlag(t *testing.T) {
	flags := parseGlobalFlags([]string{"tie", "-f", "/tmp/x.tie", "get", "k"})
	if flags.dbFile != "/tmp/x.tie" {
		t.Errorf("-f = %q", flags.dbFile)
	}
	if flags.configExplicit {
		t.Error("-f was mistaken for an explicit config")
	}
	if flags := parseGlobalFlags([]string{"tie", "--file=/d/y.tie", "dump"}); flags.dbFile != "/d/y.tie" {
		t.Errorf("--file= = %q", flags.dbFile)
	}
	if flags := parseGlobalFlags([]string{"tie", "get", "-f", "tag"}); flags.collection != "" || flags.dbFile != "" {
		t.Errorf("a subcommand -f leaked into globals: collection=%q file=%q", flags.collection, flags.dbFile)
	}
	flags = parseGlobalFlags([]string{"tie", "-f", "/tmp/x.tie", "--read-only", "--reverse-relations", "tag, parent", "get", "k"})
	if !flags.readOnly || flags.reverseRelations != "tag, parent" {
		t.Errorf("read-only/reverse flags = %+v", flags)
	}
	if flags := parseGlobalFlags([]string{"tie", "--read-only=false", "get", "k"}); flags.readOnly {
		t.Error("--read-only=false was parsed as true")
	}
	rels, err := parseRelations(" tag, , parent ")
	if err != nil || len(rels) != 2 || rels[0] != "tag" || rels[1] != "parent" {
		t.Errorf("parseRelations = %v, %v", rels, err)
	}
	if _, err := parseRelations(" , "); err == nil {
		t.Error("an empty reverse-relations list was accepted")
	}
}

// TestDumpFileRefusesRunningServer: the advisory lock makes the old "do not
// run this against a live server" warning a hard error instead of a corrupt
// dump.
func TestDumpFileRefusesRunningServer(t *testing.T) {
	env := testenv.StartT(t)
	cfg := client.Config{
		TripleStoreURL: env.TripleStoreURL,
		Namespace:      "Collections",
		Collection:     "Main",
	}
	tc := client.NewTieClientFor(cfg, "")
	defer tc.Close()
	if _, err := tc.Add("abc", "tag", "jazz"); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(env.DBDir, "Collections", "Main.tie")
	_, _, code, runErr := runTie(t, &app{}, "", "dump", "--file", file)
	if code != exitOperational {
		t.Fatalf("dump exited %d, want %d (operational)", code, exitOperational)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "locked by another process") {
		t.Errorf("err = %v, want the lock message", runErr)
	}
}

// TestReadOnlyCLI runs the command tree against a read-only direct database
// while a write-capable client still holds the file open.
func TestReadOnlyCLI(t *testing.T) {
	a, file := localApp(t)
	if _, _, code, err := runTie(t, a, "", "add", "abc", "tag", "jazz"); code != 0 {
		t.Fatalf("add exited %d: %v", code, err)
	}

	roCfg, err := client.ConfigForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	roCfg.ReadOnly = true
	ro := client.NewTieClientFor(roCfg, "")
	t.Cleanup(func() { _ = ro.Close() })
	roApp := &app{config: roCfg, tie: ro}

	out, _, code, err := runTie(t, roApp, "", "get", "-r", "jazz")
	if code != 0 {
		t.Fatalf("read-only get exited %d: %v", code, err)
	}
	if !strings.Contains(out, "abc\ttag\tjazz") {
		t.Errorf("read-only get output = %q", out)
	}

	_, _, code, err = runTie(t, roApp, "", "add", "x", "tag", "y")
	if code != exitOperational {
		t.Fatalf("read-only add exited %d, want %d", code, exitOperational)
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "write requests are disabled") {
		t.Errorf("read-only add err = %v", err)
	}

	// The writer is still fine beside the reader.
	if _, _, code, err := runTie(t, a, "", "add", "abc2", "tag", "jazz"); code != 0 {
		t.Fatalf("writer after read-only open exited %d: %v", code, err)
	}
}

// TestConfigureDBFilePrecedence covers the cases main has to get right: a
// missing config is fine, a broken explicit one is not, and an existing config
// contributes filehosts while -f contributes the database.
func TestConfigureDBFilePrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Images.tie")
	if err := os.WriteFile(file, nil, 0o666); err != nil {
		t.Fatal(err)
	}

	// No config at all.
	_, missing := client.LoadConfig(filepath.Join(dir, "absent.toml"))
	cfg, err := configureDBFile(client.Config{}, missing, globalFlags{dbFile: file})
	if err != nil {
		t.Fatalf("missing config with -f: %v", err)
	}
	if cfg.TripleStoreFile != file || cfg.Collection != "Images" {
		t.Errorf("cfg = %+v", cfg)
	}

	// An explicit broken config stays an error.
	if _, err := configureDBFile(client.Config{}, missing, globalFlags{dbFile: file, configExplicit: true}); err == nil {
		t.Error("an explicit missing -C was ignored")
	}

	// An existing config keeps its filehosts and templates.
	base := client.Config{
		TripleStoreURL:   "http://store.example:1161",
		DefaultFileHosts: []string{"default"},
		FileHosts:        map[string]client.FileHost{"default": {URL: "http://blobs.example:1162"}},
		ImportDest:       map[string]string{"audio-dir": "/music/{album}"},
		PrevVersions:     7,
	}
	cfg, err = configureDBFile(base, nil, globalFlags{dbFile: file, readOnly: true, reverseRelations: "tag,parent"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TripleStoreFile != file || cfg.TripleStoreURL != "" {
		t.Errorf("triplestore = %q / %q", cfg.TripleStoreURL, cfg.TripleStoreFile)
	}
	if len(cfg.FileHosts) != 1 || cfg.ImportDest["audio-dir"] != "/music/{album}" || cfg.PrevVersions != 7 {
		t.Errorf("config fields were lost: %+v", cfg)
	}
	if !cfg.ReadOnly || len(cfg.ReverseRelations) != 2 {
		t.Errorf("direct flags were lost: readOnly=%v reverse=%v", cfg.ReadOnly, cfg.ReverseRelations)
	}

	// -c names a sibling in the same directory.
	cfg, err = configureDBFile(base, nil, globalFlags{dbFile: file, collection: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	resolved := cfg.ResolveCollection("Other")
	if resolved.TripleStoreFile != filepath.Join(dir, "Other.tie") {
		t.Errorf("resolved file = %q", resolved.TripleStoreFile)
	}

	// A same-named [Collections] entry must not redirect the pinned file.
	withEntry := base
	withEntry.Collections = map[string]client.CollectionEntry{"Images": {Collection: "Archive2024"}}
	cfg, err = configureDBFile(withEntry, nil, globalFlags{dbFile: file})
	if err != nil {
		t.Fatal(err)
	}
	resolved = cfg.ResolveCollection("")
	if resolved.Collection != "Images" || resolved.TripleStoreFile != file {
		t.Errorf("pinned entry resolved to %q / %q", resolved.Collection, resolved.TripleStoreFile)
	}
}
