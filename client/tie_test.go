package client

import (
	"fmt"
	"testing"
)

func TestAdd(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	tie.Add("heyhey", "Noice", "Oh yeah!", func(reply AddReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
}

func TestGet(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	tie.Add("heyhey", "Noice", "Oh yeah!", func(reply AddReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
	tie.Get("heyhey", func(reply GetReply) {
		val2 := reply.Result["heyhey"]["Noice"]
		if !val2.Has("Oh yeah!") || !reply.Success {
			t.Error(reply.Message)
		}
	})
}

func TestDelete(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	tie.Add("heyhey", "Noice", "Oh yeah!", func(reply AddReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
	tie.Delete("heyhey", "Noice", "Oh yeah!", func(reply DeleteReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
	tie.Get("heyhey", func(reply GetReply) {
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
	tie.Add("Heyhey", "Noice", "Oh yeah", func(reply AddReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})

	// Update with new value
	update := tie.NewUpdate("Heyhey", "Noice", "Oh yeah", "Oh yeah 2")
	tie.Update(update, func(reply UpdateReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})

	// Check that you can get the new value
	tie.Get("Heyhey", func(reply GetReply) {
		val2 := reply.Result["Heyhey"]["Noice"]
		if !val2.Has("Oh yeah 2") || !reply.Success {
			t.Error(reply.Message)
		}
	})

	// Check that the value is gone
	tie.Get("Heyhey", func(reply GetReply) {
		val2 := reply.Result["Heyhey"]["Noice"]
		if val2.Has("Oh yeah") && reply.Success {
			t.Error(reply.Message)
		}
	})

	// Clean up for next run
	tie.Delete("Heyhey", "Noice", "Oh yeah 2", func(reply DeleteReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
	})
}

func TestBatch(t *testing.T) {
	config := TestingConfig()
	tie := NewTieClient(config)
	b := tie.NewBatch()

	b.Add("heyhey", "Noice14", "woopwoop")
	b.Add("heyhey", "Noice17", "woopwoop6")
	b.Get("heyhey")

	tie.Batch(b, func(reply BatchReply) {
		if !reply.Success {
			t.Error(reply.Message)
		}
		for _, x := range reply.GetReplys {
			x.Result.ForEachValue2(func(key, val1, val2 string) {
				fmt.Println(key, val1, val2)
			})
		}
	})
}
