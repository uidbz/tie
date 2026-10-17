package webservice

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/uidbz/tie/auth"
)

func newTestWebservice(t *testing.T) *Webservice {
	t.Helper()
	return NewWebservice(WebserviceConfig{
		DbPath: t.TempDir(),
		Auth:   auth.NewStore(nil, auth.RoleWrite),
	}, nil)
}

// TestCollectionKeyRejectsTraversal pins the namespace/collection validation:
// both ids arrive straight from the request and become filesystem paths, so
// traversal names must be refused before any join.
func TestCollectionKeyRejectsTraversal(t *testing.T) {
	ws := newTestWebservice(t)
	bad := [][2]string{
		{"../../home/x", "col"},
		{"Collections", "../../evil"},
		{"Collections", ".."},
		{"Collections", "a/b"},
		{"Collections", "a\\b"},
		{"", "col"},
		{"Collections", ""},
		{"Collections", ".hidden-start"},
		{"Collections", "x\x00y"},
	}
	for _, ns := range bad {
		if _, err := ws.collectionKey(ns[0], ns[1]); err == nil {
			t.Errorf("collectionKey(%q, %q): want error, got nil", ns[0], ns[1])
		}
	}
	good := [][2]string{
		{"Collections", "Main"},
		{"someones-namespace", "someones-collection"},
		{"Collections", "Main_prev"},
		{"Collections", "v1.2"},
	}
	for _, ns := range good {
		if _, err := ws.collectionKey(ns[0], ns[1]); err != nil {
			t.Errorf("collectionKey(%q, %q): unexpected error %v", ns[0], ns[1], err)
		}
	}
}

// TestReadDoesNotCreateCollection: a read-role resolution of an unknown
// collection must report not-found and leave no file, directory or writer
// behind. Only write requests may create.
func TestReadDoesNotCreateCollection(t *testing.T) {
	ws := newTestWebservice(t)

	col, err := ws.GetCollection("Collections", "probe-me", false)
	if !errors.Is(err, ErrCollectionNotFound) {
		t.Errorf("read of unknown collection: err = %v, want ErrCollectionNotFound", err)
	}
	if col != nil {
		t.Error("read of unknown collection returned a collection")
	}
	// Nothing was created on disk.
	if _, err := os.Stat(filepath.Join(ws.DbPath("Collections"), "probe-me.tie")); !os.IsNotExist(err) {
		t.Errorf("read created the collection file: %v", err)
	}
	if _, err := os.Stat(ws.DbPath("Collections")); !os.IsNotExist(err) {
		t.Error("read created the namespace directory")
	}

	// A write resolution creates it, and the read then finds it.
	col, err = ws.GetCollection("Collections", "probe-me", true)
	if err != nil || col == nil {
		t.Fatalf("write resolution failed: %v", err)
	}
	col.Add("k", "rel", "v")
	col.Sync()
	col, err = ws.GetCollection("Collections", "probe-me", false)
	if err != nil || col == nil {
		t.Fatalf("read after create failed: %v", err)
	}

	// A fresh webservice (cold cache) also finds the on-disk collection on read.
	ws2 := NewWebservice(WebserviceConfig{DbPath: ws.Config.DbPath, Auth: auth.NewStore(nil, auth.RoleWrite)}, nil)
	col, err = ws2.GetCollection("Collections", "probe-me", false)
	if err != nil || col == nil {
		t.Fatalf("cold read of existing collection failed: %v", err)
	}
}

// TestDropRejectsTraversal: Drop must not delete .tie files outside DbPath.
func TestDropRejectsTraversal(t *testing.T) {
	ws := newTestWebservice(t)
	if err := ws.DropCollection("../../escape", "victim"); err == nil {
		t.Error("Drop accepted a traversal namespace")
	}
	if err := ws.DropCollection("Collections", "../../escape/victim"); err == nil {
		t.Error("Drop accepted a traversal collection")
	}
}
