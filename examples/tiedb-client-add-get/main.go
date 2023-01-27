package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/api"
	"git.sr.ht/~uid/tie/client"
)

func HandleAddError(reply *api.AddReply) {
	if !reply.Success {
		fmt.Println("Error adding triple to database:", reply.Message)
	}
}

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)

	tie.Add("MyKey", "Category", "Some value 2", HandleAddError)
	tie.Add("MyKey", "Category", "Some other value 2", HandleAddError)
	tie.Add("MyKey", "AnotherCategory", "Value 333", HandleAddError)

	tie.Get("MyKey", func(reply *api.GetReply) {
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
