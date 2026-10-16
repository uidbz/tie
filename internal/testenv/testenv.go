// Package testenv starts the two servers a tie integration test needs — a real
// triplestore (webservice + api over a temp database) and a faithful filehost
// stand-in — inside the test binary, so the suite never skips and never needs
// test-env/start.sh.
//
// It is a normal package rather than a _test.go file so more than one package's
// tests can share one harness.
package testenv

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
	tiedb "github.com/uidbz/tie/tiedb"
	"github.com/uidbz/tie/webservice"
)

// Env is a running pair of servers.
type Env struct {
	// TripleStoreURL is the webservice endpoint (POST /{request}).
	TripleStoreURL string
	// FileHostURL is the blob store endpoint (PUT /upload/{hash}, GET/HEAD /{hash}).
	FileHostURL string
}

// Start launches both on a fresh temp database. The returned cleanup stops the
// servers and removes the temp dir.
func Start() (*Env, func(), error) {
	// The triplestore logs one line per request; in a test that is thousands of
	// lines of noise around the failures that matter.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})))

	dbDir, err := os.MkdirTemp("", "tie-test-db-")
	if err != nil {
		return nil, nil, err
	}

	c := api.CollectionInfo{}
	requests := []webservice.RequestInterface{
		c.NewDummyRequest(),
		c.NewQueryRequest(),
		c.NewExpandRequest(nil, ""),
		c.NewSetRequest("", "", nil),
		c.NewAddRequest("", "", ""),
		c.NewDeleteRequest("", "", ""),
		c.NewUpdateRequest(api.Update{}),
		api.NewBatchRequest(&api.Batch{}),
		c.NewAssociatedRequest(""),
		c.NewSyncRequest(),
		c.NewDumpRequest(),
		c.NewDropRequest(),
		c.NewCoTagsRequest(nil, nil, ""),
		c.NewCheckIndexRequest(false, false),
		c.NewMkDirRequest(tiedb.DirSpec{}),
		api.NewVersionRequest(),
	}
	ws := webservice.NewWebservice(webservice.WebserviceConfig{
		DbPath: dbDir,
		Auth:   auth.NewStore(nil, auth.RoleWrite),
	}, requests)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{request}", ws.RequestHandler)
	wsSrv := httptest.NewServer(mux)

	fh := &fileHost{blobs: map[string][]byte{}}
	fhSrv := httptest.NewServer(fh.routes())

	return &Env{TripleStoreURL: wsSrv.URL, FileHostURL: fhSrv.URL}, func() {
		wsSrv.Close()
		fhSrv.Close()
		os.RemoveAll(dbDir)
	}, nil
}

// StartT starts the environment and cleans it up when the test ends.
func StartT(tb testing.TB) *Env {
	tb.Helper()
	env, close, err := Start()
	if err != nil {
		tb.Fatalf("testenv: starting: %v", err)
	}
	tb.Cleanup(close)
	return env
}

// fileHost is a minimal, faithful implementation of the tie-filehost contract
// the client exercises: content-addressed PUT (with checksum verification),
// HEAD existence, and GET download. Blobs live in memory.
type fileHost struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func (f *fileHost) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload", f.upload)
	mux.HandleFunc("PUT /upload/{hash}", f.upload)
	mux.HandleFunc("GET /{hash}", f.get)
	mux.HandleFunc("HEAD /{hash}", f.get)
	mux.HandleFunc("GET /{hash}/{filename}", f.get)
	return mux
}

func (f *fileHost) upload(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if hash != "" && !metadata.IsHexHash(hash) {
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	got, err := metadata.HashReader(bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if hash != "" && got != hash {
		http.Error(w, "Checksum mismatch", http.StatusUnprocessableEntity)
		return
	}
	f.mu.Lock()
	f.blobs[got] = body
	f.mu.Unlock()
	fmt.Fprint(w, got)
}

func (f *fileHost) get(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !metadata.IsHexHash(hash) {
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	body, ok := f.blobs[hash]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Write(body)
}
