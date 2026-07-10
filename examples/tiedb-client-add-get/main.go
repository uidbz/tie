package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)

	add := func(key, value1, value2 string) {
		if _, err := tie.Add(key, value1, value2); err != nil {
			fmt.Println("Error adding triple to database:", err)
		}
	}

	add("pizza", "topping", "tomato")
	add("pizza", "topping", "cheese")
	add("pizza", "topping", "basil")
	add("pizza", "baking-time", "7 min")
	add("pizza", "baking-temperature", "250 °C")
	tie.Sync()

	reply, err := tie.Get("pizza", client.GetOptions{})
	if err != nil {
		fmt.Println("Error getting result:", err)
		return
	}
	// Print all values
	reply.Result.ForEachValue2(func(key, value1, value2 string) {
		fmt.Println(value2)
	})
	// Print all value2s in a "category"/value1
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
}
