package main

import (
	"encoding/json"
	"fmt"

	"gopkg.in/resty.v1"
)

func printOutput(resp *resty.Response, err error) {
	if verbose {
		fmt.Println("Response from Web Service:")
		fmt.Println(string(resp.Body()))
	}
	if err != nil {
		fmt.Println("Error communicating with web service:", resp, err)
	}
}

func SendToWebservice(command string, body json.RawMessage, handler func(json.RawMessage)) {
	resp, err := resty.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		SetResult(AuthSuccess{}).
		Post(state.Webservice + "/" + state.Namespace + "/" + state.Collection + "/" + command)

	printOutput(resp, err)
	handler(resp.Body())
}
