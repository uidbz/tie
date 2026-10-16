package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreatedConfigHasNoCredentials is the §5.1 L17 guard: the auto-created
// default config used to carry defaultuser/defaultpassword, writing a password
// to disk that no server is configured to accept.
func TestCreatedConfigHasNoCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c, created, err := LoadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected the config to be created")
	}
	if c.Username != "" || c.Password != "" {
		t.Errorf("created config carries credentials %q/%q, want none", c.Username, c.Password)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"defaultuser", "defaultpassword"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("the written config still mentions %q", secret)
		}
	}
}
