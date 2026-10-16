package main

import (
	"fmt"
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
		if !present {
			// Store predates the dir index: pay the full scan once here rather
			// than on every reaper pass.
			di.bootstrap(path)
		} else {
			// Pick up any manifest whose index line was lost to a crash.
			di.repair(path)
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
