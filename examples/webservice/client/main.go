package main

import (
	"fmt"

	"github.com/uidbz/tie/examples/webservice/api"

	ws "github.com/uidbz/tie/webservice"
)

func main() {
	client, err := ws.NewClient("http://localhost:8080", "myuser", "mypassword", false)
	if err != nil {
		fmt.Println(err)
		return
	}

	request := api.NewDummyRequest()
	request.SomeData = "Test"

	if reply, err := client.Run(request); err != nil {
		fmt.Println(err)
	} else {
		data := ws.ReadReply[api.DummyReply](reply)
		if data.Success {
			fmt.Println(data.Hello)
		} else {
			fmt.Println("Request failed")
		}
	}
}
