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
}

var (
	stores       []*blobStore
	defaultStore *blobStore
)

// initStores builds the package-level store set from config. When BlobPaths is
// empty it synthesizes a single "default" store from the legacy scalar BlobPath
// (permanent retention), preserving single-store behavior. The default store —
// the one named "default", else the first listed — receives uploads that carry
// no Tie-Store header.
func initStores(cfg FilehostConfig) error {
	specs := cfg.BlobPaths
	if len(specs) == 0 {
		specs = []BlobPathConfig{{Name: "default", Path: cfg.BlobPath}}
	}
	stores = nil
	defaultStore = nil
	seen := map[string]bool{}
	for _, s := range specs {
		if s.Name == "" {
			return fmt.Errorf("blob store with empty Name")
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate blob store name %q", s.Name)
		}
		seen[s.Name] = true
		if s.Path == "" {
			return fmt.Errorf("blob store %q has empty Path", s.Name)
		}
		var d time.Duration
		if s.DefaultRetention != "" && s.DefaultRetention != "infinite" {
			var err error
			if d, err = time.ParseDuration(s.DefaultRetention); err != nil {
				return fmt.Errorf("blob store %q: invalid DefaultRetention: %w", s.Name, err)
			}
		}
		path := filepath.Clean(s.Path)
		ri, err := loadRetentionIndex(path)
		if err != nil {
			return fmt.Errorf("blob store %q: loading retention index: %w", s.Name, err)
		}
		st := &blobStore{name: s.Name, path: path, defaultRetention: d, retention: ri}
		stores = append(stores, st)
		if s.Name == "default" {
			defaultStore = st
		}
	}
	if defaultStore == nil {
		defaultStore = stores[0]
	}
	return nil
}

// storeByName returns the named store, or the default store when name is empty.
// It returns nil for an unknown non-empty name.
func storeByName(name string) *blobStore {
	if name == "" {
		return defaultStore
	}
	for _, s := range stores {
		if s.name == name {
			return s
		}
	}
	return nil
}

// findBlob returns the store holding hash, searching all stores (first hit
// wins). Content addresses are globally unique, so at most one store holds a
// given hash under normal operation.
func findBlob(hash string) (*blobStore, bool) {
	for _, s := range stores {
		if _, err := os.Stat(PathFromHash(s.path, hash)); err == nil {
			return s, true
		}
	}
	return nil, false
}

// makeDestinationPath returns the on-disk path for hash within st, creating the
// shard directories.
func (st *blobStore) makeDestinationPath(hash string) (string, error) {
	dest := PathFromHash(st.path, hash)
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
