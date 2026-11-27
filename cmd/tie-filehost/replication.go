package main

// Replication read-side primitives (#159): a stable, paginated keylist per
// store and a batch membership check. With them, reconciling two stores is
// a set difference followed by ordinary uploads to the existing PUT path,
// whose dedup fast path makes re-sending present blobs free anyway.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/uidbz/tie/metadata"
)

const (
	defaultKeysLimit = 1000
	maxKeysLimit     = 10000
	maxHasHashes     = 10000
)

// keysReply is the GET /-/keys/{store} body. Keys are in ascending hash
// order; Next is the cursor for the following page ("" on the last page).
type keysReply struct {
	Store string   `json:"store"`
	Keys  []string `json:"keys"`
	Next  string   `json:"next"`
}

// keysHandler answers GET /-/keys/{store}?cursor=&limit=: the blob hashes
// of one store in ascending order, strictly after cursor. The order is the
// shard layout's own (lexical by hash), so a page boundary is stable across
// restarts and a shard-parallel reconciler can split the key space by
// prefix. The listing is a point-in-time walk: blobs added or reaped while
// paging may or may not appear, which a set-difference sync tolerates
// (a missed new blob is caught next round; a reaped one fails its upload
// check harmlessly).
func (s *server) keysHandler(w http.ResponseWriter, r *http.Request) {
	st := s.storeByName(r.PathValue("store"))
	if st == nil {
		http.Error(w, "Unknown store: "+r.PathValue("store"), http.StatusNotFound)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !metadata.IsHexHash(cursor) {
		http.Error(w, "cursor must be a content address", http.StatusBadRequest)
		return
	}
	limit := defaultKeysLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(n, maxKeysLimit)
	}
	keys, more, err := listKeysAfter(st.path, cursor, limit)
	if err != nil {
		http.Error(w, "listing store: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reply := keysReply{Store: st.name, Keys: keys}
	if reply.Keys == nil {
		reply.Keys = []string{}
	}
	if more && len(keys) > 0 {
		reply.Next = keys[len(keys)-1]
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply)
}

// listKeysAfter walks root in lexical order and returns up to limit blob
// hashes greater than cursor, and whether more remain. Shard directories
// whose prefix sorts entirely before the cursor are skipped without being
// read, so a late page costs about as much as an early one.
func listKeysAfter(root, cursor string, limit int) (keys []string, more bool, err error) {
	errDone := errors.New("done")
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil // a shard vanished mid-walk (reaper): skip it
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			prefix := strings.ReplaceAll(filepath.ToSlash(rel), "/", "")
			if strings.Count(filepath.ToSlash(rel), "/")+1 > shardLevels || !isHexPrefix(prefix) {
				return fs.SkipDir // not a shard directory
			}
			if cursor != "" && prefix < cursor[:min(len(prefix), len(cursor))] {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !metadata.IsHexHash(name) || name <= cursor || filepath.Dir(rel) == "." {
			return nil
		}
		if len(keys) == limit {
			more = true
			return errDone
		}
		keys = append(keys, name)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errDone) {
		return nil, false, walkErr
	}
	return keys, more, nil
}

func isHexPrefix(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return s != ""
}

// hasRequest / hasReply are the POST /-/has bodies.
type hasRequest struct {
	Hashes []string `json:"hashes"`
}

type hasReply struct {
	Store   string   `json:"store"`
	Present []string `json:"present"`
	Absent  []string `json:"absent"`
}

// hasHandler answers POST /-/has: which of the given hashes the store
// (Tie-Store header, else the default store) holds. One stat per hash and
// no body transfer, so a replicator can diff thousands of keys per request.
func (s *server) hasHandler(w http.ResponseWriter, r *http.Request) {
	st := s.storeByName(r.Header.Get("Tie-Store"))
	if st == nil {
		http.Error(w, "Unknown store: "+r.Header.Get("Tie-Store"), http.StatusBadRequest)
		return
	}
	var req hasRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(maxHasHashes)*80)).Decode(&req); err != nil {
		http.Error(w, "malformed request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Hashes) > maxHasHashes {
		http.Error(w, "too many hashes (max "+strconv.Itoa(maxHasHashes)+")", http.StatusRequestEntityTooLarge)
		return
	}
	reply := hasReply{Store: st.name, Present: []string{}, Absent: []string{}}
	for _, h := range req.Hashes {
		p, err := PathFromHash(st.path, h)
		if err != nil {
			http.Error(w, "not a content address: "+h, http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(p); err == nil {
			reply.Present = append(reply.Present, h)
		} else {
			reply.Absent = append(reply.Absent, h)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply)
}
