package fleetcmd

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestParseFlagsInterspersed pins that flags after positionals are
// parsed (the usage text shows `specs edit FILE --set …` and
// `autostart set NAME --host H`; the flag package alone silently treated
// them as positionals), that "--" ends flag parsing, and that a bad flag
// or -h is an ErrUsage error rather than an os.Exit.
func TestParseFlagsInterspersed(t *testing.T) {
	newFS := func() (*flag.FlagSet, *string, *[]string) {
		fs := flag.NewFlagSet("t", flag.ExitOnError) // parseFlags must override ExitOnError
		host := fs.String("host", "", "")
		var sets stringList
		fs.Var(&sets, "set", "")
		return fs, host, (*[]string)(&sets)
	}

	fs, host, sets := newFS()
	pos, err := parseFlags(fs, []string{"FILE", "--set", "a:1:2:0", "NAME", "--host", "H", "--set", "b:1:1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"FILE", "NAME"}) || *host != "H" || len(*sets) != 2 {
		t.Fatalf("pos=%q host=%q sets=%q", pos, *host, *sets)
	}

	fs, host, _ = newFS()
	pos, err = parseFlags(fs, []string{"a", "--", "--host", "x"})
	if err != nil || !reflect.DeepEqual(pos, []string{"a", "--host", "x"}) || *host != "" {
		t.Fatalf("after --: pos=%q host=%q err=%v", pos, *host, err)
	}

	fs, _, _ = newFS()
	fs.SetOutput(new(strings.Builder))
	if _, err := parseFlags(fs, []string{"--bogus"}); !errors.Is(err, ErrUsage) {
		t.Fatalf("bad flag: err = %v, want ErrUsage", err)
	}
	fs, _, _ = newFS()
	fs.SetOutput(new(strings.Builder))
	if _, err := parseFlags(fs, []string{"-h"}); !errors.Is(err, ErrUsage) {
		t.Fatalf("-h: err = %v, want ErrUsage", err)
	}
	if err := noArgs(fs, []string{"stray"}); !errors.Is(err, ErrUsage) {
		t.Fatalf("noArgs: %v", err)
	}
}

// TestRunInitKeepsUnparsableConfig pins that init does not overwrite an
// existing config that merely fails to parse.
func TestRunInitKeepsUnparsableConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Chdir(t.TempDir()) // no config in the current dir
	brand = TieBrand
	path := filepath.Join(dir, brand.CLIName, configFile())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const broken = "[[Hosts]\nName = 'x'\n"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInit(nil); err == nil {
		t.Fatal("runInit overwrote (or accepted) an existing unparsable config")
	}
	if b, _ := os.ReadFile(path); string(b) != broken {
		t.Fatalf("config was modified: %q", b)
	}
}

// TestBuildScriptStampsDescribeVersion pins that rollout stamps the
// git-describe version (vN.M…, parseable by the version gates) instead
// of the --rev argument ("master"), and no longer swallows a failed
// fast-forward.
func TestBuildScriptStampsDescribeVersion(t *testing.T) {
	if !strings.Contains(buildScript, "-X main.Version=$VERSION") || !strings.Contains(buildScript, "git describe --tags") {
		t.Fatal("build script does not stamp the describe version")
	}
	if strings.Contains(buildScript, "|| true") {
		t.Fatal("build script still swallows a failed pull")
	}
	if strings.Count(buildScript, "%s") != 3 {
		t.Fatalf("build script placeholders changed: %d", strings.Count(buildScript, "%s"))
	}
	if c, err := builtCommit("noise\nBUILT abc1234\n"); err != nil || c != "abc1234" {
		t.Fatalf("builtCommit = %q, %v", c, err)
	}
	if _, err := builtCommit("go: build failed"); err == nil {
		t.Fatal("builtCommit accepted output without a BUILT line")
	}
}

// TestSetAutostartStaysInOwnTable pins that an added key lands in the
// service's own [[DeployedImages]] table, not in a sub-table or a
// top-level table that follows it.
func TestSetAutostartStaysInOwnTable(t *testing.T) {
	src := "[[DeployedImages]]\nName = 'svc'\nPort = 1\n\n[DeployedImages.Env]\nA = '1'\n\n[JobTypeSpecs.CPUv1]\nCPU = 1\n"
	out, _, missing := SetAutostart(src, []string{"svc", "zz", "aa"})
	if !reflect.DeepEqual(missing, []string{"aa", "zz"}) {
		t.Fatalf("missing = %q, want sorted", missing)
	}
	key := strings.Index(out, "ServiceAutostart = true")
	if key < 0 || key > strings.Index(out, "[DeployedImages.Env]") {
		t.Fatalf("key not in the main table:\n%s", out)
	}
}

// TestSameBase pins the AdminToken host match: runner1 must not match
// runner10.
func TestSameBase(t *testing.T) {
	cases := []struct {
		url, base string
		want      bool
	}{
		{"https://runner1", "https://runner1", true},
		{"https://runner1/", "https://runner1", true},
		{"https://runner1/api", "https://runner1", true},
		{"https://runner10", "https://runner1", false},
		{"https://runner1", "", false},
	}
	for _, c := range cases {
		if got := sameBase(c.url, c.base); got != c.want {
			t.Errorf("sameBase(%q, %q) = %v", c.url, c.base, got)
		}
	}
}
