package main

//TODO: Update this to the new way

import (
	"fmt"

	"git.sr.ht/~uid/tie/api"
	"git.sr.ht/~uid/tie/client"
	// "git.sr.ht/~uid/tie/request"
)

// func BatchHandler(reply *request.Reply, err error) {
// 	if err != nil {
// 		fmt.Println("Error handling 'Add' reponse:", err.Error())
// 		fmt.Println("Received:", string(reply.ReplyRawResponse))
// 	}

// 	var result = *reply.DataBatch()

// 	for _, x := range result.Add {
// 		fmt.Println("Success:", x.Success, "Message:", x.Message)
// 	}

// 	for _, x := range result.Get {
// 		for _, y := range x {
// 			for i, _ := range y.Value2 {
// 				key := y.Item
// 				value1 := y.Value1[i]
// 				value2 := y.Value2[i]

// 				fmt.Println(key + "\t" + value1 + "\t" + value2)
// 			}
// 		}
// 	}
// }

func main() {
	config := client.DefaultConfig()
	tie := client.NewTieClient(config)
	col := tie.CollectionInfo()

	batch := api.Batch{}
	batch.Add = append(batch.Add, col.NewAddRequest("MyKey", "Some value 1", "Some value 2"))
	batch.Add = append(batch.Add, col.NewAddRequest("MyKey2", "Some value 1", "Some value 2"))
	batch.Add = append(batch.Add, col.NewAddRequest("MyKey3", "Some value 1", "Some value 2"))

	batch.Get = append(batch.Get, col.NewGetRequest("MyKey"))
	batch.Get = append(batch.Get, col.NewGetRequest("MyKey2"))
	batch.Get = append(batch.Get, col.NewGetRequest("MyKey3"))

	// a := request.NewAddRequest(
	// b := request.NewAddRequest("MyKey2", "Some value 1", "Some value 2")
	// c := request.NewAddRequest("MyKey3", "Some value 1", "Some value 2")

	// d := request.NewGetRequest([]string{"MyKey"})
	// e := request.NewGetRequest([]string{"MyKey2"})
	// f := request.NewGetRequest([]string{"MyKey3"})

	tie.Batch(&batch, func(reply *api.BatchReply) {
		if !reply.Success {
			fmt.Println(fmt.Println("Error happened:", reply.Message))
			return
		}
		for _, x := range reply.Add {
			fmt.Println("Success:", x.Success, "Message:", x.Message)
		}
		// del := []*api.DeleteRequest{}
		for _, x := range reply.Get {
			x.Result.ForEachValue2(func(key, val1, val2 string) {
				// del = append(del, col.NewDeleteRequest(key, val1, val2))
				fmt.Println(key, val1, val2)
			})
		}
		// b2 := api.Batch{Delete: del}
		// tie.Batch(&b2, func(r *api.BatchReply) {})
	})

}
