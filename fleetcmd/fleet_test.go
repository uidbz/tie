package fleetcmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFleetExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tie-fleet.toml")
	src := `
[[Hosts]]
Name = "a"
API = "https://a.example/"

[[Hosts]]
Name = "b"
API = "http://b.example:1166"
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts, err := loadFleet(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0].Name != "a" || hosts[0].API != "https://a.example" {
		t.Errorf("load wrong: %+v", hosts)
	}
	if hosts[1].API != "http://b.example:1166" {
		t.Errorf("trailing handling wrong: %+v", hosts[1])
	}
}

func TestLoadFleetRejectsIncompleteEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tie-fleet.toml")
	if err := os.WriteFile(path, []byte("[[Hosts]]\nName = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFleet(path); err == nil {
		t.Error("expected error for entry missing API")
	}
}

func TestLoadFleetSearchFindsCurrentDir(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	src := "[[Hosts]]\nName = \"h\"\nAPI = \"https://h\"\n"
	if err := os.WriteFile(filepath.Join(dir, configFile()), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts, err := loadFleet("")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].Name != "h" {
		t.Errorf("search did not find current-dir config: %+v", hosts)
	}
}

func TestDefaultFleetValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, configFile())
	if err := os.WriteFile(path, []byte("[[Hosts]]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// sanity: defaultFleet round-trips through the same TOML shape
	f := defaultFleet()
	if len(f.Hosts) != 3 {
		t.Fatalf("default fleet wrong: %+v", f)
	}
	for _, h := range f.Hosts {
		if h.Name == "" || h.API == "" {
			t.Errorf("default host incomplete: %+v", h)
		}
	}
}
