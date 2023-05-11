# tie is a collection of programs and libraries to tie things together

tie is written in Go (golang)

Programs:
* tie-daemon is a webservice which provides an interface to a tie database
* tie is CLI client that can talk with a tie-daemon
* tie-fileserver is a content addressed file server
* tie-upload can upload files and directories to a tie-fileserver
* tie-download can download files and directories from a tie-fileserver

Packages:
* tiedb is an in-memory triplestore.
* client can talk with a tie-daemon
* io/getlib can download files from a tie-fileserver
* io/putlib can upload files to a tie-fileserver

## tiedb

tiedb is an in-memory triplestore, which is also written to disk for persistence.

The idea was originally to store trains of thought, but it has not directly succeeded in doing this so far.
The premise is that every-thing is an association. The word is the thing. All words are explained by associations to other words. The relation between the associations is an association as well.

That gives us:
thing association relation

thing, association, relation is the same thing which I call an association, because no thing can stand alone; it only makes sense in relation to something else.

So what we need is 2 things: Self/other, full/empty, key/value.
But for convenience, lets throw in a 'relation', which is just another 'value' which is just another 'key', which is just another 'value'.

To initiate a look up, you need a key - so the first value is called 'key'. Association and Relation are just values (and keys), so I call them Value1 and Value2.
These 3 go together. I call this a 'triple'.

When querying the database the result is a TripleSet which essentially is a map, within a map, within a map - coupled with helper functions to read the Result more easily.

Here is a full program that shows how it works - see 'examples' for more.

``` Go
package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
)

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)

	handleError := func(reply client.AddReply) {
		if !reply.Success {
			fmt.Println("Error adding triple to database:", reply.Message)
		}
	}

	tie.Add("pizza", "topping", "tomato", handleError)
	tie.Add("pizza", "topping", "cheese", handleError)
	tie.Add("pizza", "topping", "basil", handleError)
	tie.Add("pizza", "baking-time", "7 min", handleError)
	tie.Add("pizza", "baking-temperature", "250 °C", handleError)

	tie.Get("pizza", func(reply client.GetReply) {
		if reply.Success {
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
		} else {
			fmt.Println("Error getting result:", reply.Message)
		}
	})
}
```
Example output (notice that the order is not retained):
```
basil
cheese
tomato
7 min
250 °C
```
