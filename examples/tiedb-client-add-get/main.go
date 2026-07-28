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

	// Attrs fetches a single key's forward attributes as one flat Row.
	row, err := tie.Attrs("pizza")
	if err != nil {
		fmt.Println("Error getting result:", err)
		return
	}
	// Print all values across every relation.
	for _, values := range row.Attributes {
		for _, value2 := range values {
			fmt.Println(value2)
		}
	}
	// Print all values under one relation.
	for _, value2 := range client.RowValues(row, "topping") {
		fmt.Println(value2)
	}
	// Read a relation known to hold a single value.
	if value2, ok := client.RowOne(row, "baking-time"); ok {
		fmt.Println(value2)
	}
	// RowFirst returns the first value (or "" if absent).
	fmt.Println(client.RowFirst(row, "baking-temperature"))
}
