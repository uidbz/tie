package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)
	batch := tie.NewBatch()

	batch.Add("MyKey", "Some value 1", "Some value 2")
	batch.Add("MyKey2", "Some value 1", "Some value 2")
	batch.Add("MyKey3", "Some value 1", "Some value 2")

	batch.Get("MyKey")
	batch.Get("MyKey2")
	batch.Get("MyKey3")

	tie.Batch(batch, func(reply client.BatchReply) {
		if !reply.Success {
			fmt.Println(fmt.Println("Error happened:", reply.Message))
			return
		}
		for _, x := range reply.AddReplys {
			fmt.Println("Success:", x.Success, "Message:", x.Message)
		}
		for _, x := range reply.GetReplys {
			x.Result.ForEachValue2(func(key, val1, val2 string) {
				fmt.Println(key, val1, val2)
			})
		}
	})

}
