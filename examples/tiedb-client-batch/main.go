package main

import (
	"fmt"

	"github.com/uidbz/tie/client"
)

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)
	batch := tie.NewBatch()

	// Ops run in array order and the whole batch shares one final Sync.
	batch.Add("Bob", "age", "32")
	batch.Add("Alice", "age", "25")
	batch.Add("Pete", "age", "24")
	// Set replaces the whole (key, relation) value set in one op — here it
	// corrects Pete's age instead of leaving two values behind.
	batch.Set("Pete", "age", []string{"23"})

	batch.Add("Pizza", "topping", "tomato")
	batch.Add("Pizza", "topping", "cheese")
	batch.Add("Pizza", "topping", "basil")
	batch.Add("Pizza", "baking-time", "7 min")
	batch.Add("Pizza", "baking-temperature", "250 °C")

	// Batch now returns only an error: it succeeds as a whole or reports the
	// first failing op. There are no per-op replies to inspect.
	if _, err := tie.Batch(batch); err != nil {
		fmt.Println("Error happened:", err)
		return
	}

	// Expand fetches many keys' attributes in one round trip.
	rows, err := tie.Expand([]string{"Bob", "Alice", "Pete", "Pizza"})
	if err != nil {
		fmt.Println("Error reading back:", err)
		return
	}
	for _, row := range rows {
		for relation, values := range row.Attributes {
			for _, value2 := range values {
				fmt.Println(row.Key, relation, value2)
			}
		}
	}
	// Print each key's single age value, if it has exactly one.
	for _, row := range rows {
		if ages := client.RowValues(row, "age"); len(ages) == 1 {
			fmt.Println(row.Key, "is", ages[0])
		}
	}
}
