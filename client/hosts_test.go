package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hostTestConfig is a config with two filehosts and one collection that
// overrides them: the bound collection writes to "default", "coll" writes to
// "slow".
func hostTestConfig() Config {
	return Config{
		Namespace:        "nstest",
		Collection:       "main",
		DefaultFileHosts: []string{"default"},
		FileHosts:        map[string]FileHost{"default": {URL: "http://fast.example"}, "slow": {URL: "http://slow.example"}},
		Collections:      map[string]CollectionEntry{"coll": {Collection: "coll", FileHosts: []string{"slow"}}},
		TripleStoreURL:   "http://127.0.0.1:1",
	}
}

// TestResolveHostAgreesWithResolveHosts is the §8 S11 guard: the two
// resolution paths used to answer separately, so a caller that asked one for
// "the host" and the other for "the hosts" could get different answers.
func TestResolveHostAgreesWithResolveHosts(t *testing.T) {
	tc := NewTieClientFor(hostTestConfig(), "")

	names := tc.ResolveHosts("", nil)
	if len(names) == 0 {
		t.Fatal("ResolveHosts returned no hosts")
	}
	want, ok := tc.Config.FileHosts[names[0]]
	if !ok {
		t.Fatalf("ResolveHosts named %q with no [FileHosts] entry", names[0])
	}
	got, err := tc.ResolveHost("")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ResolveHost(\"\") = %+v, but ResolveHosts names %q first", got, names[0])
	}
}

// TestResolveHostHonoursTheName: an explicit host must be used, not the
// collection's default.
func TestResolveHostHonoursTheName(t *testing.T) {
	tc := NewTieClientFor(hostTestConfig(), "")

	got, err := tc.ResolveHost("slow")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://slow.example" {
		t.Errorf("ResolveHost(\"slow\") = %+v", got)
	}
	if _, err := tc.ResolveHost("nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("ResolveHost(\"nope\") = %v, want an error naming the host", err)
	}
}

// TestResolveHostInUsesTheCollection: a collection that names its own filehost
// must be resolved to that host. This is what `tie verify -c other
// --check-blobs` got wrong: the blob check asked the bound collection's host,
// so it reported every blob of the other collection as missing.
func TestResolveHostInUsesTheCollection(t *testing.T) {
	tc := NewTieClientFor(hostTestConfig(), "")

	got, err := tc.ResolveHostIn("coll", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://slow.example" {
		t.Errorf("ResolveHostIn(\"coll\", \"\") = %+v, want the collection's own host", got)
	}
	// The bound collection is unaffected.
	if def, err := tc.ResolveHostIn("", ""); err != nil || def.URL != "http://fast.example" {
		t.Errorf("ResolveHostIn(\"\", \"\") = %+v, %v", def, err)
	}
	// An explicit name still wins over the collection's list.
	if named, err := tc.ResolveHostIn("coll", "default"); err != nil || named.URL != "http://fast.example" {
		t.Errorf("ResolveHostIn(\"coll\", \"default\") = %+v, %v", named, err)
	}
}

// TestResolveHostWithoutAnyFileHost: the error has to say what to fix.
func TestResolveHostWithoutAnyFileHost(t *testing.T) {
	cfg := hostTestConfig()
	cfg.DefaultFileHosts = nil
	cfg.Collections = nil
	tc := NewTieClientFor(cfg, "")
	if _, err := tc.ResolveHost(""); err == nil || !strings.Contains(err.Error(), "no filehost configured") {
		t.Errorf("ResolveHost(\"\") = %v, want the configuration error", err)
	}
	if _, err := tc.BlobClient(""); err == nil {
		t.Error("BlobClient(\"\") succeeded without any filehost")
	}
}

// TestCheckBlobsAsksTheVerifiedCollection is the concrete consequence: verify
// of a collection that names its own filehost used to ask the *client's*
// collection's host instead, so every blob came back "missing".
func TestCheckBlobsAsksTheVerifiedCollection(t *testing.T) {
	absent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer absent.Close()
	present := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3")
	}))
	defer present.Close()

	cfg := hostTestConfig()
	cfg.FileHosts["default"] = FileHost{URL: absent.URL}
	cfg.FileHosts["slow"] = FileHost{URL: present.URL}
	tc := NewTieClientFor(cfg, "")

	rows := []Row{{Key: strings.Repeat("ab", 32)}}

	missing, err := tc.checkBlobsExist("coll", rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("checkBlobsExist(\"coll\") reported %v missing, but the collection's filehost holds them", missing)
	}

	// The bound collection still resolves to its own host, which does not have
	// the blob: the check is not simply always answering "present".
	missing, err = tc.checkBlobsExist("", rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 {
		t.Errorf("checkBlobsExist(\"\") reported %v missing, want the one blob", missing)
	}
}
