package client

// In-process test environment: every client integration test runs against a
// real triplestore (webservice + api, disk-backed, temp dir) and a faithful
// filehost stand-in started inside the test binary, so the suite never skips
// and never needs test-env/start.sh. Set TIE_TEST_WEBSERVICE /
// TIE_TEST_FILEHOST to run against external servers instead.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/auth"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/webservice"
)

// testFileHost is a minimal, faithful implementation of the tie-filehost
// contract the client exercises: content-addressed PUT (with checksum
// verification), HEAD existence, and GET download. Blobs live in memory.
type testFileHost struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func (f *testFileHost) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload", f.upload)
	mux.HandleFunc("PUT /upload/{hash}", f.upload)
	mux.HandleFunc("GET /{hash}", f.get)
	mux.HandleFunc("HEAD /{hash}", f.get)
	mux.HandleFunc("GET /{hash}/{filename}", f.get)
	return mux
}

func (f *testFileHost) upload(w http.ResponseWriter, r *http.Request) {
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

func (f *testFileHost) get(w http.ResponseWriter, r *http.Request) {
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

func TestMain(m *testing.M) {
	if ws, fh := os.Getenv("TIE_TEST_WEBSERVICE"), os.Getenv("TIE_TEST_FILEHOST"); ws != "" && fh != "" {
		testingTripleStoreURL, testingFileHostURL = ws, fh
		os.Exit(m.Run())
	}

	dbDir, err := os.MkdirTemp("", "tie-client-test-db")
	if err != nil {
		panic(err)
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
		api.NewVersionRequest(),
	}
	ws := webservice.NewWebservice(webservice.WebserviceConfig{
		DbPath: dbDir,
		Auth:   auth.NewStore(nil, auth.RoleWrite),
	}, requests)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{request}", ws.RequestHandler)
	wsSrv := httptest.NewServer(mux)

	fh := &testFileHost{blobs: map[string][]byte{}}
	fhSrv := httptest.NewServer(fh.routes())

	testingTripleStoreURL, testingFileHostURL = wsSrv.URL, fhSrv.URL
	code := m.Run()
	wsSrv.Close()
	fhSrv.Close()
	os.RemoveAll(dbDir)
	os.Exit(code)
}
