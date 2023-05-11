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

	tie.Get("pizza", func(reply client.GetReply) {
		if reply.Success {
			cat := reply.Result["pizza"]["topping"]
			cat.ForEach(func(value2 string) {
				fmt.Println(value2)
			})
			// One way of reading the result
			if value2, ok := reply.Result["pizza"]["baking-time"].One(); ok {
				fmt.Println(value2)
			}
			// Here is a shortcut to the same function
			if value2, ok := reply.OneValue2("baking-temperature"); ok {
				fmt.Println(value2)
			}
		} else {
			fmt.Println("Error getting result:", reply.Message)
		}
	})
}
