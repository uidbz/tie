package client

import (
	"errors"
	"fmt"
	"testing"
)

func TestAdd(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	if _, err := tie.Add("heyhey", "Noice", "Oh yeah!"); err != nil {
		t.Error(err)
	}
}

func TestGet(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	if _, err := tie.Add("heyhey", "Noice", "Oh yeah!"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}
	reply, err := tie.SimpleGet("heyhey")
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Result["heyhey"]["Noice"].Has("Oh yeah!") {
		t.Error("expected value not found")
	}
}

func TestDelete(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	if _, err := tie.Add("heyhey", "Noice", "Oh yeah!"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}
	if _, err := tie.Delete("heyhey", "Noice", "Oh yeah!"); err != nil {
		t.Error(err)
	}
	if e := tie.Sync(); e != nil {
		t.Error(e)
	}
	reply, err := tie.SimpleGet("heyhey")
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Error(err)
	}
	if reply != nil && reply.Result["heyhey"]["Noice"].Has("Oh yeah!") {
		t.Error("value should have been deleted")
	}
}

func TestUpdate(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)

	// Add a tripple that can be updated
	if _, err := tie.Add("Heyhey", "Noice", "Oh yeah"); err != nil {
		t.Error(err)
	}

	if e := tie.Sync(); e != nil {
		t.Error(e)
	}

	// Update with new value
	update := tie.NewUpdate("Heyhey", "Noice", "Oh yeah", "Oh yeah 2")
	if _, err := tie.Update(update); err != nil {
		t.Error(err)
	}

	if e := tie.Sync(); e != nil {
		t.Error(e)
	}

	// Check that the new value is present and the old one is gone
	reply, err := tie.SimpleGet("Heyhey")
	if err != nil {
		t.Fatal(err)
	}
	val2 := reply.Result["Heyhey"]["Noice"]
	if !val2.Has("Oh yeah 2") {
		t.Error("new value not found")
	}
	if val2.Has("Oh yeah") {
		t.Error("old value should be gone")
	}

	// Clean up for next run
	if _, err := tie.Delete("Heyhey", "Noice", "Oh yeah 2"); err != nil {
		t.Error(err)
	}
}

func TestBatch(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	b := tie.NewBatch()

	b.Add("heyhey", "Noice14", "woopwoop")
	b.Add("heyhey", "Noice17", "woopwoop6")
	b.Get("heyhey")

	reply, err := tie.Batch(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range reply.GetReplys {
		x.Result.ForEachValue2(func(key, val1, val2 string) {
			fmt.Println(key, val1, val2)
		})
	}
}
