package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

type Pizza struct {
	Uid               string
	BakingTimeInMin   int
	BakingTemperature string
	WeightInGrams     float64
	Topping           []string
}

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)
	manager := client.NewObjectManager[Pizza](tie, "pizzas")

	pizza1 := Pizza{
		Uid:               "pizza1",
		BakingTimeInMin:   7,
		BakingTemperature: "250 °C",
		WeightInGrams:     350.55,
		Topping: []string{
			"tomato",
			"cheese",
			"basil",
		},
	}
	pizza2 := Pizza{
		Uid:               "pizza2",
		BakingTimeInMin:   7,
		BakingTemperature: "250 °C",
		WeightInGrams:     350.55,
		Topping: []string{
			"tomato",
			"cheese",
			"mushrooms",
		},
	}

	check := func(err error) {
		if err != nil {
			fmt.Println("Error from database:", err.Error())
		}
	}

	printPizzas := func(msg string) {
		fmt.Println("--", msg)
		pizzas, err := manager.GetAll()
		check(err)
		for _, x := range pizzas {
			fmt.Println(x)
		}
	}

	check(manager.Add(pizza1))
	check(manager.Add(pizza2))
	printPizzas("Added pizza1 and pizza2")

	pizza2.Topping = append(pizza2.Topping, "more cheese")
	pizza2.BakingTimeInMin = 8
	check(manager.Upsert(pizza2))
	printPizzas("Changed pizza2: More cheese + longer baking time")

	fmt.Println("Pizzas with mushrooms:")
	pizzas, err := manager.Associated("mushrooms")
	check(err)
	for _, x := range pizzas {
		fmt.Println(x)
	}

	check(manager.Delete(pizza2))
	printPizzas("Deleted pizza2")

}
