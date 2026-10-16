package client

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// batchFailingProxy fronts the test triplestore and refuses every Batch, so a
// repair run reaches its mutating step and fails there.
func batchFailingProxy(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Batch") {
			http.Error(w, "batch refused", http.StatusInternalServerError)
			return
		}
		req, err := http.NewRequest(r.Method, testingTripleStoreURL+r.URL.Path, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if u, p, ok := r.BasicAuth(); ok {
			req.SetBasicAuth(u, p)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRepairJournalOnlyRecordsAppliedOps is the §5.1 L16 guard: the journal
// line was written and Applied incremented before the batch was sent, so a
// failed batch left a journal claiming mutations that never happened — and the
// journal is the record an operator would trust to undo or replay a repair.
func TestRepairJournalOnlyRecordsAppliedOps(t *testing.T) {
	tie := freshVerifyClient(t)
	dir := t.TempDir()
	writeFile(t, dir+"/a.txt", "keep me")
	if err := tie.ImportDir(dir, tie.Config.FileHosts["default"], "directory", nil, "", 0); err != nil {
		t.Fatalf("ImportDir: %v", err)
	}
	root, err := tie.DirUIDFromPath(FileURIScheme + dir)
	if err != nil || root == "" {
		t.Fatalf("DirUIDFromPath: uid=%q err=%v", root, err)
	}
	// A blobless leftover: repair's plan is to delete its triples.
	bare := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := tie.Add(bare, str(TieTypeProperty), str(TieFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := tie.Add(bare, str(TieParent), string(root)); err != nil {
		t.Fatal(err)
	}
	if err := tie.Sync(); err != nil {
		t.Fatal(err)
	}

	rep, err := tie.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	// Dry-run first: the plan is what the failing run would have journaled.
	plan, err := tie.RepairTree(rep, RepairOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Planned) == 0 {
		t.Fatal("this tree produced no repair ops; the test would prove nothing")
	}

	cfg := tie.Config
	cfg.TripleStoreURL = batchFailingProxy(t).URL
	broken := NewTieClientFor(cfg, "")

	var journal bytes.Buffer
	res, err := broken.RepairTree(rep, RepairOptions{Apply: true, Journal: &journal})
	if err == nil {
		t.Fatal("a refused Batch was reported as success")
	}
	if journal.Len() != 0 {
		t.Errorf("journal claims operations that never happened:\n%s", journal.String())
	}
	if res != nil && res.Applied != 0 {
		t.Errorf("Applied = %d, want 0 after a failed batch", res.Applied)
	}
}
