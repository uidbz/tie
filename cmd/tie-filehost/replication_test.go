package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/uidbz/tie/io/fhclient"
	"github.com/uidbz/tie/metadata"
)

// putTestBlobs uploads n distinct blobs and returns their hashes, sorted.
func putTestBlobs(t *testing.T, h *testHost, n int) []string {
	t.Helper()
	var hashes []string
	for i := range n {
		body := []byte(fmt.Sprintf("blob %d", i))
		hash, err := metadata.HashReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPut, h.hs.URL+"/upload/"+hash, bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("upload %d: %v %v", i, err, resp)
		}
		resp.Body.Close()
		hashes = append(hashes, hash)
	}
	slices.Sort(hashes)
	return hashes
}

func getKeys(t *testing.T, url string) (keysReply, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r keysReply
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			t.Fatal(err)
		}
	}
	return r, resp.StatusCode
}

// GET /-/keys/{store} pages through every blob in ascending order with a
// stable cursor, and validates its parameters.
func TestKeysPagination(t *testing.T) {
	h := newTestServer(t)
	want := putTestBlobs(t, h, 7)

	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination does not terminate")
		}
		r, code := getKeys(t, h.hs.URL+"/-/keys/default?limit=3&cursor="+cursor)
		if code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if len(r.Keys) > 3 {
			t.Fatalf("page of %d keys, limit 3", len(r.Keys))
		}
		got = append(got, r.Keys...)
		if r.Next == "" {
			break
		}
		cursor = r.Next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("keys = %v\nwant   %v", got, want)
	}

	// A cursor past everything yields an empty last page.
	r, _ := getKeys(t, h.hs.URL+"/-/keys/default?cursor="+want[len(want)-1])
	if len(r.Keys) != 0 || r.Next != "" {
		t.Fatalf("after the last key: %+v", r)
	}
	for _, bad := range []string{"/-/keys/nope", "/-/keys/default?cursor=xyz", "/-/keys/default?limit=0"} {
		if _, code := getKeys(t, h.hs.URL+bad); code == http.StatusOK {
			t.Errorf("%s: status 200, want an error", bad)
		}
	}
}

// POST /-/has splits the request into present and absent hashes.
func TestHasBatch(t *testing.T) {
	h := newTestServer(t)
	present := putTestBlobs(t, h, 2)
	absent := strings.Repeat("ab", 32)
	body, _ := json.Marshal(hasRequest{Hashes: []string{present[0], absent, present[1]}})
	resp, err := http.Post(h.hs.URL+"/-/has", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r hasReply
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Present, []string{present[0], present[1]}) || !slices.Equal(r.Absent, []string{absent}) {
		t.Fatalf("has = %+v", r)
	}

	bad, _ := json.Marshal(hasRequest{Hashes: []string{"../../etc/passwd"}})
	resp2, err := http.Post(h.hs.URL+"/-/has", "application/json", bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-hash entry: status %d, want 400", resp2.StatusCode)
	}
}

// The fhclient Keys/Has helpers speak these endpoints (end to end against
// the real handlers).
func TestFhclientKeysAndHas(t *testing.T) {
	h := newTestServer(t)
	want := putTestBlobs(t, h, 5)
	c := fhclient.New(h.hs.URL, nil)
	var got []string
	cursor := ""
	for {
		page, err := c.Keys(context.Background(), "default", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Keys...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Keys = %v, want %v", got, want)
	}
	missing := strings.Repeat("cd", 32)
	present, absent, err := c.Has(context.Background(), []string{want[0], missing})
	if err != nil || !slices.Equal(present, want[:1]) || !slices.Equal(absent, []string{missing}) {
		t.Fatalf("Has = %v %v %v", present, absent, err)
	}
}
