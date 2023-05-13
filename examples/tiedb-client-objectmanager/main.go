package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

type Pizza struct {
	Uid               string
	BakingTime        string
	BakingTemperature string
	Topping           []string
}

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)
	manager := client.NewObjectManager[Pizza](tie)

	pizza1 := Pizza{
		Uid:               "pizza1",
		BakingTime:        "7 min",
		BakingTemperature: "250 °C",
		Topping: []string{
			"tomato",
			"cheese",
			"basil",
		},
	}
	pizza2 := Pizza{
		Uid:               "pizza2",
		BakingTime:        "7 min",
		BakingTemperature: "250 °C",
		Topping: []string{
			"tomato",
			"cheese",
			"mushrooms",
		},
	}

	check := func(err error) {
		if err != nil {
			fmt.Println("Error adding triple to database:", err.Error())
		}
	}
	check(manager.Add(pizza1))
	check(manager.Upsert(pizza2))

	printPizzas := func() {
		pizzas, err := manager.GetAll()
		check(err)
		for _, x := range pizzas {
			fmt.Println(x)
		}
	}
	printPizzas()

	pizza2.Topping = append(pizza2.Topping, "more cheese")
	pizza2.BakingTime = "8 min"
	check(manager.Upsert(pizza2))

	printPizzas()
}
