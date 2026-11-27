package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// #174: settings resolve from -settings FILE, then a filehost hash
// (-settings-hash / TIE_SETTINGS_HASH), then inline TIE_SETTINGS; the app
// always receives a file.
func TestResolveSettings(t *testing.T) {
	const blob = `{"tolerance":0.005}`
	hash := strings.Repeat("ab", 32)
	fh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+hash {
			w.Write([]byte(blob))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fh.Close)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	p, done, err := resolveSettings(externalApp{settings: "/given.json", settingsHash: hash}, env(nil))
	if err != nil || p != "/given.json" {
		t.Fatalf("-settings must win: %q %v", p, err)
	}
	done()

	for name, tc := range map[string]struct {
		app externalApp
		env map[string]string
	}{
		"flag hash": {externalApp{filehost: fh.URL, settingsHash: hash}, nil},
		"env hash":  {externalApp{filehost: fh.URL}, map[string]string{"TIE_SETTINGS_HASH": hash, "TIE_SETTINGS": `{"x":1}`}},
		"env json":  {externalApp{filehost: fh.URL}, map[string]string{"TIE_SETTINGS": blob}},
	} {
		p, done, err := resolveSettings(tc.app, env(tc.env))
		if err != nil || read(p) != blob {
			t.Errorf("%s: path %q content %q err %v", name, p, read(p), err)
		}
		done()
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s: cleanup left %s", name, p)
		}
	}

	if p, _, err := resolveSettings(externalApp{}, env(nil)); err != nil || p != "" {
		t.Fatalf("no settings: %q %v", p, err)
	}
	for name, e := range map[string]map[string]string{
		"bad json":    {"TIE_SETTINGS": "{nope"},
		"bad hash":    {"TIE_SETTINGS_HASH": "../x"},
		"absent blob": {"TIE_SETTINGS_HASH": strings.Repeat("cd", 32)},
	} {
		if _, _, err := resolveSettings(externalApp{filehost: fh.URL}, env(e)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
