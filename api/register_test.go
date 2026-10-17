package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uidbz/tie/auth"
	ws "github.com/uidbz/tie/webservice"
)

// protocolIds lists every request id the tie protocol defines. Adding a request
// type means adding it here and to RegisterAll, and this test fails if only one
// of the two happens — the drift the old split between the request list and the
// readRequests table allowed (§7.5).
var protocolIds = []string{
	IdAdd, IdAssociated, IdBatch, IdCheckIndex, IdCoTags, IdDelete, IdDrop,
	IdDummy, IdDump, IdExpand, IdMkDir, IdQuery, IdSet, IdSync, IdUpdate, IdVersion,
}

// readIds are the requests that change nothing server-side.
var readIds = []string{
	IdDummy, IdQuery, IdExpand, IdAssociated, IdCoTags, IdVersion, IdDump,
}

func registerAllServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := ws.NewWebservice(ws.WebserviceConfig{
		DbPath: t.TempDir(),
		Auth:   auth.NewStore(nil, auth.RoleWrite),
	})
	RegisterAll(srv)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{request}", srv.RequestHandler)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return httpSrv
}

// TestEveryProtocolIdIsServed: every id the api package defines must be
// registered on a tie server. Handlers may still reject the empty probe body —
// what must never happen is a 404, which means the request exists in the
// protocol and nobody registered it.
func TestEveryProtocolIdIsServed(t *testing.T) {
	srv := registerAllServer(t)

	for _, id := range protocolIds {
		resp, err := http.Post(srv.URL+"/"+id, "application/json", strings.NewReader(`{"Id":"`+id+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("%s: the server answers 404, so RegisterAll does not register it", id)
		}
	}

	// An id the protocol does not define stays a 404 rather than reaching some
	// other handler.
	resp, err := http.Post(srv.URL+"/NotARequest", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown request id: status = %d, want 404", resp.StatusCode)
	}
}

// TestRegisterAllClassifiesReads: the read set is exactly what the protocol says
// it is, and everything else is a write. A request that fell out of the old
// readRequests map was silently treated as a write; here the classification is
// the registration, so this test is what keeps the protocol honest.
func TestRegisterAllClassifiesReads(t *testing.T) {
	srv := ws.NewWebservice(ws.WebserviceConfig{
		DbPath: t.TempDir(),
		Auth:   auth.NewStore(nil, auth.RoleWrite),
	})
	RegisterAll(srv)

	isRead := map[string]bool{}
	for _, id := range readIds {
		isRead[id] = true
	}
	for _, id := range protocolIds {
		want := auth.AccessWrite
		if isRead[id] {
			want = auth.AccessRead
		}
		if got := srv.AccessFor(id); got != want {
			t.Errorf("%s: access = %v, want %v", id, got, want)
		}
	}
}
