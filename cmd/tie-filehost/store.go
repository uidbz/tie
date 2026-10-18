package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// BlobPathConfig configures one physical store in FilehostConfig.BlobPaths.
type BlobPathConfig struct {
	Name string
	Path string
	// DefaultRetention is applied to uploads to this store that carry no
	// Tie-Retention header: a Go duration (e.g. "72h"), or empty/"infinite" for
	// permanent.
	DefaultRetention string
}

// blobStore is one physical content-addressed store: a root directory plus its
// own retention index and the default retention for uploads that don't specify
// one. A multi-store filehost hosts several; dedup is per-store.
type blobStore struct {
	name             string
	path             string
	defaultRetention time.Duration // 0 = infinite
	retention        *retentionIndex
	// dirs records which blobs in this store are tie-directory manifests, so
	// the reaper's mark phase walks those instead of opening every blob.
	dirs *dirIndex
}

// claimExisting reports whether a blob is already on disk and, when it is,
// records the upload's retention — both while holding the retention lock. The
// reaper's sweep deletes blobs and prunes their entries under that same lock, so
// an upload that sees the bytes present cannot have them removed before its
// entry is written.
//
// Without this, a dedup re-upload racing a sweep loses data silently: the upload
// stats the blob (present), the sweep deletes it and drops the entry, the upload
// then records retention for bytes that are gone and answers 200 — and the
// triple store keeps triples pointing at nothing. With the claim, either the
// upload lands first (the entry is re-extended and the sweep's re-check skips the
// blob) or the sweep lands first (the claim sees no file and the caller stores
// the body, which it still has).
//
// When the blob is absent nothing is recorded and the caller proceeds to write
// the body.
func (s *blobStore) claimExisting(hash string, expiresAt int64, token string, now int64) (claimed bool, isDir bool, err error) {
	path, err := PathFromHash(s.path, hash)
	if err != nil {
		return false, false, err
	}
	s.retention.mu.Lock()
	defer s.retention.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		return false, false, nil
	}
	if err := s.retention.recordUploadLocked(hash, expiresAt, token, now, true); err != nil {
		return true, false, err
	}
	return true, isDirBlobAt(s, hash), nil
}

// server is the filehost: its physical stores and the read cache. Handlers are
// methods on it, so there is no package-level state to reset between tests and
// no way for one handler to skip a step another one does — the duplication that
// let a {hash} route forget validation (§7.1).
type server struct {
	stores []*blobStore
	// def receives uploads that carry no Tie-Store header.
	def *blobStore
	// cache is the optional read cache (nil when disabled).
	cache *blobCache
}

// initStores builds the store set from config. When BlobPaths is empty it
// synthesizes a single "default" store from the legacy scalar BlobPath
// (permanent retention), preserving single-store behavior. The default store —
// the one named "default", else the first listed — receives uploads that carry
// no Tie-Store header.
func (s *server) initStores(cfg FilehostConfig) error {
	specs := cfg.BlobPaths
	if len(specs) == 0 {
		specs = []BlobPathConfig{{Name: "default", Path: cfg.BlobPath}}
	}
	s.stores = nil
	s.def = nil
	seen := map[string]bool{}
	for _, sp := range specs {
		if sp.Name == "" {
			return fmt.Errorf("blob store with empty Name")
		}
		if seen[sp.Name] {
			return fmt.Errorf("duplicate blob store name %q", sp.Name)
		}
		seen[sp.Name] = true
		if sp.Path == "" {
			return fmt.Errorf("blob store %q has empty Path", sp.Name)
		}
		var d time.Duration
		if sp.DefaultRetention != "" && sp.DefaultRetention != "infinite" {
			var err error
			if d, err = time.ParseDuration(sp.DefaultRetention); err != nil {
				return fmt.Errorf("blob store %q: invalid DefaultRetention: %w", sp.Name, err)
			}
		}
		path := filepath.Clean(sp.Path)
		ri, err := loadRetentionIndex(path)
		if err != nil {
			return fmt.Errorf("blob store %q: loading retention index: %w", sp.Name, err)
		}
		di, present, err := loadDirIndex(path)
		if err != nil {
			return fmt.Errorf("blob store %q: loading dir index: %w", sp.Name, err)
		}
		if !di.ready {
			// The store has no completed dir index — it predates the file, or
			// a scan was interrupted. Do not pay for the scan here: it opens
			// every blob in the store, and start-up is the one place that must
			// not block, because until the listener is open the filehost
			// answers nothing. The reaper builds it before its first mark
			// phase, and refuses to sweep until it exists.
			slog.Info("dir index incomplete; the reaper will build it before its first pass",
				"store", sp.Name, "path", path, "partial", present)
		}
		st := &blobStore{name: sp.Name, path: path, defaultRetention: d, retention: ri, dirs: di}
		s.stores = append(s.stores, st)
		if sp.Name == "default" {
			s.def = st
		}
	}
	if s.def == nil {
		s.def = s.stores[0]
	}
	return nil
}

// storeByName returns the named store, or the default store when name is empty.
// It returns nil for an unknown non-empty name.
func (s *server) storeByName(name string) *blobStore {
	if name == "" {
		return s.def
	}
	for _, st := range s.stores {
		if st.name == name {
			return st
		}
	}
	return nil
}

// findBlob returns the store holding hash, searching all stores (first hit
// wins). Content addresses are globally unique, so at most one store holds a
// given hash under normal operation.
func (s *server) findBlob(hash string) (*blobStore, bool) {
	for _, st := range s.stores {
		p, err := PathFromHash(st.path, hash)
		if err != nil {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return st, true
		}
	}
	return nil, false
}

// makeDestinationPath returns the on-disk path for hash within st, creating the
// shard directories.
func (st *blobStore) makeDestinationPath(hash string) (string, error) {
	dest, err := PathFromHash(st.path, hash)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", err
	}
	return dest, nil
}

// uploadExpiry resolves the absolute unix expiry for an upload to st: the
// Tie-Retention header if present, else the store's DefaultRetention, else
// permanent (0).
func (st *blobStore) uploadExpiry(r *http.Request, now time.Time) (int64, error) {
	if r.Header.Get("Tie-Retention") != "" {
		return parseRetention(r, now)
	}
	if st.defaultRetention > 0 {
		return now.Add(st.defaultRetention).Unix(), nil
	}
	return 0, nil
}
