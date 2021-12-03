package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/request"
)

func AddHandler(reply *request.Reply, err error) {
	if err != nil {
		fmt.Println("Error handling 'Add' reponse:", err.Error())
		fmt.Println("Received:", string(reply.ReplyRawResponse))
	}

	var result = *reply.DataStatus()
	fmt.Println("Success:", result.Success, "Message:", result.Message)
}

func GetHandler(reply *request.Reply, err error) {
	if err != nil {
		fmt.Println("Error handling 'Get' reponse:", err.Error())
		fmt.Println("Received:", string(reply.ReplyRawResponse))
	}

	var result = *reply.DataGet()
	for _, x := range result {
		for i, _ := range x.Value2 {
			key := x.Item
			value1 := x.Value1[i]
			value2 := x.Value2[i]

			fmt.Println(key + "\t" + value1 + "\t" + value2)
		}
	}
}

func main() {
	tie.InitConfig()

	a := request.Add{
		Key:    "MyKey",
		Value1: "Some value 1",
		Value2: "Some value 2",
	}
	AddHandler(tie.Run(request.RequestTypeAdd, a))

	b := request.Get{
		Values: []string{"MyKey"},
	}
	GetHandler(tie.Run(request.RequestTypeGet, b))
}
