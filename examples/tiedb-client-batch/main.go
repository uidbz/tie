package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)
	batch := tie.NewBatch()

	batch.Add("Bob", "age", "32")
	batch.Add("Alice", "age", "25")
	batch.Add("Pete", "age", "24")
	batch.Add("Pete", "age", "23") // Adding this will cause the last loop to skip Pete

	batch.Add("Pizza", "topping", "tomato")
	batch.Add("Pizza", "topping", "cheese")
	batch.Add("Pizza", "topping", "basil")
	batch.Add("Pizza", "baking-time", "7 min")
	batch.Add("Pizza", "baking-temperature", "250 °C")

	batch.Get("Bob")
	batch.Get("Alice")
	batch.Get("Pete")
	batch.Get("Pizza")

	reply, err := tie.Batch(batch)
	if err != nil {
		fmt.Println("Error happened:", err)
		return
	}
	for _, x := range reply.AddReplys {
		fmt.Println("Success:", x.Success, "Message:", x.Message)
	}
	// Print all values that was added
	for _, x := range reply.GetReplys {
		x.Result.ForEachValue2(func(key, val1, val2 string) {
			fmt.Println(key, val1, val2)
		})
	}
	// Print age, if only 1 age exists
	for _, x := range reply.GetReplys {
		if val2, ok := x.OneValue2("age"); ok {
			fmt.Println(val2)
		}

	}
}
