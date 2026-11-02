package images

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistryUpsertGetDelete(t *testing.T) {
	var saved [][]Image
	fail := false
	r := New(func(imgs []Image) error {
		if fail {
			return errors.New("disk full")
		}
		saved = append(saved, append([]Image{}, imgs...))
		return nil
	})

	if _, ok := r.Get("missing"); ok {
		t.Fatal("missing image found")
	}
	if !r.Upsert(Image{Name: "a:v1", Owner: "alice", DeployKey: "k1"}) {
		t.Fatal("insert failed")
	}
	got, ok := r.Get("a:v1")
	if !ok || got.Owner != "alice" {
		t.Fatalf("get after insert: %+v %v", got, ok)
	}
	// Get returns a copy: mutating it must not touch the registry.
	got.Owner = "mallory"
	if again, _ := r.Get("a:v1"); again.Owner != "alice" {
		t.Error("Get returned a live pointer")
	}

	if !r.Upsert(Image{Name: "a:v1", Owner: "bob", DeployKey: "k1"}) {
		t.Fatal("update failed")
	}
	got, _ = r.Get("a:v1")
	if got.Owner != "bob" || len(r.List()) != 1 {
		t.Errorf("update: %+v list=%d", got, len(r.List()))
	}

	fail = true
	if r.Upsert(Image{Name: "a:v1", Owner: "carol"}) {
		t.Error("upsert must report failure when save fails")
	}
	if got, _ := r.Get("a:v1"); got.Owner != "bob" {
		t.Errorf("failed save not rolled back: %+v", got)
	}
	if r.Delete("a:v1") {
		t.Error("delete must report failure when save fails")
	}
	if _, ok := r.Get("a:v1"); !ok {
		t.Error("failed delete not rolled back")
	}
	fail = false
	if !r.Delete("a:v1") {
		t.Fatal("delete failed")
	}
	if _, ok := r.Get("a:v1"); ok {
		t.Error("image survived delete")
	}
}

func TestRegistryReplace(t *testing.T) {
	r := New(nil, Image{Name: "old"})
	if err := r.Replace([]Image{{Name: "new:v1"}, {Name: "new:v2"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("old"); ok {
		t.Error("replace kept an old entry")
	}
	if len(r.List()) != 2 {
		t.Errorf("list = %d", len(r.List()))
	}
}

func TestNewDeployKey(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		k := NewDeployKey()
		if len(k) != deployKeyLength {
			t.Fatalf("key length = %d", len(k))
		}
		if strings.ContainsAny(k, "_-!@#$%^&*") {
			t.Fatalf("key outside the alphabet: %q", k)
		}
		if seen[k] {
			t.Fatalf("deploy key collision: %q", k)
		}
		seen[k] = true
	}
}
