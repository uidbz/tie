package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/request"
)

func BatchHandler(reply *request.Reply, err error) {
	if err != nil {
		fmt.Println("Error handling 'Add' reponse:", err.Error())
		fmt.Println("Received:", string(reply.ReplyRawResponse))
	}

	var result = *reply.DataBatch()

	for _, x := range result.Add {
		fmt.Println("Success:", x.Success, "Message:", x.Message)
	}

	for _, x := range result.Get {
		for _, y := range x {
			for i, _ := range y.Value2 {
				key := y.Item
				value1 := y.Value1[i]
				value2 := y.Value2[i]

				fmt.Println(key + "\t" + value1 + "\t" + value2)
			}
		}
	}
}

func main() {
	tie.InitConfig()

	batch := request.NewBatchRequest()

	a := request.NewAddRequest("MyKey", "Some value 1", "Some value 2")
	b := request.NewAddRequest("MyKey2", "Some value 1", "Some value 2")
	c := request.NewAddRequest("MyKey3", "Some value 1", "Some value 2")

	d := request.NewGetRequest([]string{"MyKey"})
	e := request.NewGetRequest([]string{"MyKey2"})
	f := request.NewGetRequest([]string{"MyKey3"})

	batch.Add = append(batch.Add, a)
	batch.Add = append(batch.Add, b)
	batch.Add = append(batch.Add, c)

	batch.Get = append(batch.Get, d)
	batch.Get = append(batch.Get, e)
	batch.Get = append(batch.Get, f)

	BatchHandler(tie.Run(batch))
}
