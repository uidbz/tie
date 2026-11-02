package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/images"
)

func TestRegistryLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet-registry.json")
	r := Load(path)

	h1 := r.AddPending("bravo", "http://b:1163", []string{"CPUv1"})
	h2 := r.AddPending("alpha", "http://a:1163", nil)
	if r.AddPending("again", h1.URL, nil) != h1 {
		t.Error("re-registering the same URL must be idempotent")
	}
	if h1.ID == h2.ID || len(h1.ID) != 10 {
		t.Errorf("host ids: %q %q", h1.ID, h2.ID)
	}
	if r.Approve("nope") != nil {
		t.Error("unknown approve id must return nil")
	}
	approved := r.Approve(h1.ID)
	if approved == nil || approved.Status != api.HostActive || len(approved.AdminToken) != 24 {
		t.Errorf("approve: %+v", approved)
	}
	if r.Approve(h1.ID) != nil {
		t.Error("approving twice must return nil")
	}

	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("registry file mode: %v %v", fi, err)
	}
	reloaded := Load(path)
	if len(reloaded.List(false)) != 2 {
		t.Errorf("reload lost hosts: %d", len(reloaded.List(false)))
	}

	// List: sorted by name, no removed, activeOnly.
	names := []string{}
	for _, h := range r.List(false) {
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "alpha,bravo" {
		t.Errorf("list order: %v", names)
	}
	if only := r.List(true); len(only) != 1 || only[0].Name != "bravo" {
		t.Errorf("activeOnly: %+v", only)
	}
	if r.Remove(h2.ID) == nil || r.Remove(h2.ID) != nil {
		t.Error("remove semantics wrong")
	}
	if len(r.List(false)) != 1 {
		t.Error("removed host still listed")
	}
	// A removed URL may register fresh (explicit re-join).
	if r.AddPending("alpha2", h2.URL, nil) == h2 {
		t.Error("removed host must not absorb a new registration")
	}

	// Corrupt file yields a fresh registry.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(Load(path).List(false)) != 0 {
		t.Error("corrupt registry must reset")
	}
}

func TestJoin(t *testing.T) {
	var posts atomic.Int32
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fleet/register" {
			http.NotFound(w, r)
			return
		}
		var req api.RegisterRequest
		json.NewDecoder(r.Body).Decode(&req)
		n := posts.Add(1)
		switch {
		case req.JoinToken != "secret":
			http.Error(w, "bad join token", http.StatusUnauthorized)
		case n < 3:
			json.NewEncoder(w).Encode(api.RegisterReply{Status: api.HostPending, HostID: "h7"})
		default:
			json.NewEncoder(w).Encode(api.RegisterReply{
				Status: api.HostActive, HostID: "h7",
				Config: json.RawMessage(`{"Address":":443"}`),
			})
		}
	}))
	defer seed.Close()

	req := api.RegisterRequest{Name: "n", URL: "http://n:1163", JoinToken: "secret"}
	rep, err := Join(context.Background(), seed.URL+"/", req, time.Millisecond, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.HostID != "h7" || string(rep.Config) != `{"Address":":443"}` || posts.Load() != 3 {
		t.Errorf("join reply: %+v posts=%d", rep, posts.Load())
	}

	if _, err := Join(context.Background(), seed.URL, api.RegisterRequest{JoinToken: "wrong"}, time.Millisecond, time.Second, nil); err == nil ||
		!strings.Contains(err.Error(), "join token rejected") {
		t.Errorf("bad token: %v", err)
	}

	// Always-pending seed: the wait budget must surface as pending with
	// the host id for the operator.
	posts.Store(-1000) // keep the server replying pending
	rep, err = Join(context.Background(), seed.URL, req, time.Millisecond, 20*time.Millisecond, nil)
	if !errors.Is(err, ErrJoinPending) {
		t.Errorf("timeout outcome: %v", err)
	}
	var pending *PendingError
	if !errors.As(err, &pending) || pending.HostID == "" {
		t.Errorf("pending error should carry the host id: %v", err)
	}
	if rep == nil || rep.HostID == "" || rep.Status != api.HostPending {
		t.Errorf("pending reply should carry the host id: %+v", rep)
	}
}

func TestReplicate(t *testing.T) {
	var gotMethod, gotPath, gotUser string
	var gotBody images.Image
	accept := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotUser, _, _ = r.BasicAuth()
		json.NewDecoder(r.Body).Decode(&gotBody)
	}))
	defer accept.Close()
	var rejectHits atomic.Int32
	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rejectHits.Add(1)
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer reject.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	rep := &Replicator{
		Backends:   func() []string { return []string{accept.URL, reject.URL, deadURL} },
		AdminUser:  func() string { return "tie" },
		AdminToken: func() string { return "tok" },
	}
	rep.ReplicateImage(images.Image{Name: "tool:v2", DeployKey: "k"})
	rep.Wait()

	if gotMethod != http.MethodPut || gotPath != "/api/images/replicate" || gotUser != "tie" {
		t.Errorf("replication call: %s %s user=%q", gotMethod, gotPath, gotUser)
	}
	if gotBody.Name != "tool:v2" || gotBody.DeployKey != "k" {
		t.Errorf("payload: %+v", gotBody)
	}
	if rejectHits.Load() != 1 {
		t.Errorf("rejected backend hits = %d", rejectHits.Load())
	}

	// No admin token: no fan-out at all.
	quiet := &Replicator{Backends: func() []string { return []string{accept.URL, reject.URL} }}
	before := rejectHits.Load()
	quiet.ReplicateImage(images.Image{Name: "x"})
	quiet.Wait()
	if rejectHits.Load() != before {
		t.Error("replication must stop without an admin token")
	}
}

func TestForwardToSeed(t *testing.T) {
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "%s %s auth=%s:%s ok=%v", r.Method, r.URL.Path, u, p, ok)
	}))
	defer seed.Close()

	req, _ := http.NewRequest(http.MethodGet, "http://front/api/fleet/hosts", nil)
	req.SetBasicAuth("tie", "tok")
	rec := httptest.NewRecorder()
	ForwardToSeed(nil, seed.URL, rec, req)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "GET /api/fleet/hosts auth=tie:tok ok=true") {
		t.Errorf("forward result: %d %q", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	ForwardToSeed(nil, "", rec2, req)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("empty seed must 404, got %d", rec2.Code)
	}
}
