package putlib

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const license_hash = "f237c0e59ea0166af622b855b7c933cb37e25ed233048f7d85e22ef714111a02"

func TestAddressOfFile(t *testing.T) {
	pc := &PutConfig{}
	if hash, err := pc.AddressOfFile("../../LICENSE"); err != nil {
		t.Error(err)
	} else {
		if hash != license_hash {
			t.Error("Wrong license")
		}
	}
}

// A non-2xx response carries an error message, not a hash. It must surface as
// the upload error rather than being mistaken for a bad checksum.
func TestUploadNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := Upload(srv.URL, path, PutConfig{})
	if status.ErrorMsg == "" {
		t.Fatal("expected an upload error for a 403 response")
	}
	if !strings.Contains(status.ErrorMsg, "403") || !strings.Contains(status.ErrorMsg, "Forbidden") {
		t.Errorf("error should name the HTTP status and body, got %q", status.ErrorMsg)
	}
	if strings.Contains(status.ErrorMsg, "checksum") {
		t.Errorf("HTTP error must not be reported as a checksum failure: %q", status.ErrorMsg)
	}
	if status.LastItem.Hash != "" {
		t.Errorf("error body must not be taken as a hash, got %q", status.LastItem.Hash)
	}
}

// A 200 whose body is the wrong hash is still a checksum failure.
func TestUploadWrongHashIsChecksumError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("deadbeef"))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	status := Upload(srv.URL, path, PutConfig{})
	if !strings.Contains(status.ErrorMsg, "checksum") {
		t.Errorf("expected checksum failure, got %q", status.ErrorMsg)
	}
}
