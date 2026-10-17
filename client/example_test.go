package client_test

// Runnable examples for the client package. They run against the same in-process
// triplestore and filehost the client tests use (internal/testenv), so `go test`
// compiles *and* executes them: documentation that fails the build when the API
// it shows changes (§8 S13). Each example works in its own fresh collection, so
// the output below is stable.

import (
	"fmt"
	"sort"

	"github.com/uidbz/tie/client"
)

// exampleClient returns a client bound to a fresh collection.
func exampleClient(collection string) *client.TieClient {
	cfg := client.TestingConfig()
	cfg.Namespace = "examples"
	cfg.Collection = collection
	tc := client.NewTieClient(cfg)
	if err := tc.DropCollection(); err != nil {
		panic("dropping the example collection: " + err.Error())
	}
	return tc
}

func addAll(tc *client.TieClient, triples [][3]string) {
	for _, tr := range triples {
		if _, err := tc.Add(tr[0], tr[1], tr[2]); err != nil {
			panic(err)
		}
	}
}

// sorted keeps example output stable: a Row's attribute slices come back in
// insertion order, which is not what an example should demonstrate.
func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// ExampleTieClient_Add shows the write path and reading one subject back.
func ExampleTieClient_Add() {
	tc := exampleClient("ex-add")

	addAll(tc, [][3]string{
		{"pizza", "topping", "tomato"},
		{"pizza", "topping", "cheese"},
		{"pizza", "baking-time", "7 min"},
	})

	row, err := tc.Get("pizza")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	fmt.Println(sorted(client.RowValues(row, "topping")))
	fmt.Println(client.RowFirst(row, "baking-time"))

	// Output:
	// [cheese tomato]
	// 7 min
}

// ExampleTieClient_Query is the tag query: subjects carrying every term, with
// their own attributes attached. Reverse selects by the reverse index (the tag
// case), Expand attaches each match's attributes, and the int is the total
// before pagination.
func ExampleTieClient_Query() {
	tc := exampleClient("ex-query")

	addAll(tc, [][3]string{
		{"song-a", "tag", "jazz"},
		{"song-a", "tag", "blue"},
		{"song-b", "tag", "jazz"},
		{"song-c", "tag", "rock"},
	})

	rows, _, err := tc.Query(client.QuerySpec{
		Terms:   []string{"jazz", "blue"}, // AND: both tags
		Reverse: true,
		Expand:  true,
		Limit:   -1,
	})
	if err != nil {
		fmt.Println("query:", err)
		return
	}
	for _, row := range rows {
		fmt.Println(row.Key, sorted(client.RowValues(row, "tag")))
	}

	// Output:
	// song-a [blue jazz]
}

// ExampleTieClient_Set replaces the values of one relation in a single call —
// the shape `tie set` uses. Relations not named are left alone.
func ExampleTieClient_Set() {
	tc := exampleClient("ex-set")

	addAll(tc, [][3]string{
		{"pizza", "topping", "tomato"},
		{"pizza", "topping", "cheese"},
		{"pizza", "size", "large"},
	})

	if err := tc.Set("pizza", "topping", []string{"mushroom", "olive"}); err != nil {
		fmt.Println("set:", err)
		return
	}

	row, err := tc.Get("pizza")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	fmt.Println(sorted(client.RowValues(row, "topping")))
	fmt.Println(client.RowValues(row, "size"))

	// Output:
	// [mushroom olive]
	// [large]
}

// ExampleTieClient_Batch applies several writes in one round trip. The order in
// Ops is the order the server applies them.
func ExampleTieClient_Batch() {
	tc := exampleClient("ex-batch")

	addAll(tc, [][3]string{
		{"song-a", "tag", "jazz"},
		{"song-b", "tag", "jazz"},
	})

	batch := tc.NewBatch()
	batch.Add("song-a", "tag", "blue")
	batch.Delete("song-b", "tag", "jazz") // drop one tag
	batch.Set("song-a", "name", []string{"A side"})
	if _, err := tc.Batch(batch); err != nil {
		fmt.Println("batch:", err)
		return
	}

	rows, _, err := tc.Query(client.QuerySpec{Terms: []string{"jazz"}, Reverse: true, Expand: true, Limit: -1})
	if err != nil {
		fmt.Println("query:", err)
		return
	}
	for _, row := range rows {
		fmt.Println(row.Key, sorted(client.RowValues(row, "tag")), client.RowFirst(row, "name"))
	}

	// Output:
	// song-a [blue jazz] A side
}
