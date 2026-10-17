package main

import (
	"flag"
	"fmt"

	"github.com/uidbz/tie/examples/webservice/api"

	ws "github.com/uidbz/tie/webservice"
)

func main() {
	// The example server listens on :8080 by default; -url points the client at
	// another port when 8080 is taken.
	var url = flag.String("url", "http://localhost:8080", "Webservice URL.")
	flag.Parse()

	client, err := ws.NewClient(*url, "myuser", "mypassword", false)
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
