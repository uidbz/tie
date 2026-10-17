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

	// The reply type is a parameter of the call, so there is nothing to cast
	// and no prototype to keep in step with the handler.
	reply, err := ws.Call[*api.DummyRequest, api.DummyReply](client, request)
	if err != nil {
		fmt.Println(err)
		return
	}
	if reply.Success {
		fmt.Println(reply.Hello)
	} else {
		fmt.Println("Request failed")
	}
}
