package putlib

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uidbz/tie/metadata"
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

// overflowWriter errors once it has seen more than cap bytes, mimicking a
// progress bar whose max was sized from file bytes alone: a directory upload
// also streams per-directory manifests, pushing the count past max and making
// the bar return "current number exceeds max".
type overflowWriter struct {
	cap  int64
	seen int64
}

func (w *overflowWriter) Write(p []byte) (int, error) {
	w.seen += int64(len(p))
	if w.seen > w.cap {
		return len(p), errors.New("current number exceeds max")
	}
	return len(p), nil
}

// A progress writer that errors (as a real bar does on overflow) must never
// abort the upload: the error is cosmetic and would otherwise tear down the
// in-flight request, dropping the tail of the transfer.
func TestUploadSurvivesProgressWriterError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return the body's content address so checksum validation passes.
		h, err := metadata.HashReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte(h))
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), bytes.Repeat([]byte("x"), 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	// Cap well below the transfer size so the writer overflows mid-stream.
	progress := &overflowWriter{cap: 1024}
	status := Upload(srv.URL, dir, PutConfig{Progress: progress})
	if progress.seen == 0 {
		t.Fatal("progress writer never received any bytes")
	}
	if status.ErrorMsg != "" {
		t.Fatalf("upload must survive a progress writer error, got %q", status.ErrorMsg)
	}
	if len(status.UploadedItems) == 0 || status.LastItem.Hash == "" {
		t.Fatalf("expected a hash despite the progress error, got %+v", status)
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
