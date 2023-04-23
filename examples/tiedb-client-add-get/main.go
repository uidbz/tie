package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)

	HandleError := func(reply client.AddReply) {
		if !reply.Success {
			fmt.Println("Error adding triple to database:", reply.Message)
		}
	}

	tie.Add("MyKey", "Category", "Some value 2", HandleError)
	tie.Add("MyKey", "Category", "Some other value 2", HandleError)
	tie.Add("MyKey", "AnotherCategory", "Value 333", HandleError)

	tie.Get("MyKey", func(reply client.GetReply) {
		if reply.Success {
			cat := reply.Result["MyKey"]["Category"]
			cat.ForEach(func(value2 string) {
				fmt.Println(value2)
			})
			if value2, ok := reply.Result["MyKey"]["AnotherCategory"].One(); ok {
				fmt.Println(value2)
			}
		} else {
			fmt.Println("Error getting result:", reply.Message)
		}
	})
}
