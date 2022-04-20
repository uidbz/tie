package main

import (
	"fmt"

	"webservice/api"

	ws "git.sr.ht/~uid/tie/webservice"
)

func main() {
	client := ws.NewClient("http://localhost:8080", "myuser", "mypassword")

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
