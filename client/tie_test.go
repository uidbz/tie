package client

import (
	"fmt"
	"testing"

	"git.sr.ht/~uid/tie/api"
)

func TestAdd(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	tie.Add("heyhey", "Noice", "Oh yeah!", func(reply *api.AddReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
}

func TestGet(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	tie.Add("heyhey", "Noice", "Oh yeah!", func(reply *api.AddReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
	tie.Get("heyhey", func(reply *api.GetReply) {
		val2 := reply.Result["heyhey"]["Noice"]
		if !val2.Has("Oh yeah!") || !reply.Success {
			t.Error(reply.Message)
		}
	})
}

func TestDelete(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	tie.Add("heyhey", "Noice", "Oh yeah!", func(reply *api.AddReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
	tie.Delete("heyhey", "Noice", "Oh yeah!", func(reply *api.DeleteReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
	tie.Get("heyhey", func(reply *api.GetReply) {
		val2 := reply.Result["heyhey"]["Noice"]
		if val2.Has("Oh yeah!") || !reply.Success {
			t.Error(reply.Message)
		}
	})
}

func TestUpdate(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)

	// Add a tripple that can be updated
	tie.Add("Heyhey", "Noice", "Oh yeah", func(reply *api.AddReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})

	// Update with new value
	update := api.Update{
		Key:          "Heyhey",
		Value1:       "Noice",
		Value2:       "Oh yeah",
		NewValue2:    "Oh yeah 2",
		AddOnFailure: false,
	}
	tie.Update(update, func(reply *api.UpdateReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})

	// Check that you can get the new value
	tie.Get("Heyhey", func(reply *api.GetReply) {
		val2 := reply.Result["Heyhey"]["Noice"]
		if !val2.Has("Oh yeah 2") || !reply.Success {
			t.Error(reply.Message)
		}
	})

	// Check that the value is gone
	tie.Get("Heyhey", func(reply *api.GetReply) {
		val2 := reply.Result["Heyhey"]["Noice"]
		if val2.Has("Oh yeah") && reply.Success {
			t.Error(reply.Message)
		}
	})

	// Clean up for next run
	tie.Delete("Heyhey", "Noice", "Oh yeah 2", func(reply *api.DeleteReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
}

func TestBatch(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)

	b := &api.Batch{}
	ci := tie.CollectionInfo()

	b.Add = append(b.Add, ci.NewAddRequest("heyhey", "Noice14", "woopwoop"))
	b.Add = append(b.Add, ci.NewAddRequest("heyhey", "Noice17", "woopwoop6"))
	b.Get = append(b.Get, ci.NewGetRequest("heyhey"))

	tie.Batch(b, func(reply *api.BatchReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
		for _, x := range reply.Get {
			x.Result.ForEachValue2(func(key, val1, val2 string) {
				fmt.Println(key, val1, val2)
			})
		}
	})
}
