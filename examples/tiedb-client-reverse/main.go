package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)

	handleError := func(reply client.AddReply) {
		if !reply.Success {
			fmt.Println("Error adding triple to database:", reply.Message)
		}
	}

	tie.Add("pizza", "topping", "tomato", handleError)
	tie.Add("pizza", "topping", "cheese", handleError)
	tie.Add("pizza", "topping", "basil", handleError)
	tie.Add("pizza", "baking-time", "7 min", handleError)
	tie.Add("pizza", "baking-temperature", "250 °C", handleError)
	tie.Add("pizza", "type", "food", handleError)
	tie.Sync()

	tie.GetWith("cheese", client.GetOptions{Reverse: true, NextLevelValue1s: []string{"type"}}, func(reply client.GetReply) {
		if reply.Success {
			reply.ReverseResult.ForEachValue2(func(key, val1, val2 string) {
				fmt.Println("reverse", key, val1, val2)
			})
		}
	})
}
