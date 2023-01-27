package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/api"

	"github.com/spf13/cobra"
)

func cmdList() []*cobra.Command {
	cmds := []*cobra.Command{}

	// var cmdSetState = &cobra.Command{
	// 	Use:   "set [webservice] [namespace] [collection]",
	// 	Short: "Set current namespace + collection",
	// 	Long:  `Set current namespace + collection.`,
	// 	Args:  cobra.MinimumNArgs(3),
	// 	Run: func(cmd *cobra.Command, args []string) {
	// 		tie.CurrentState.Webservice = args[0]
	// 		tie.CurrentState.Namespace = args[1]
	// 		tie.CurrentState.Collection = args[2]
	// 		tie.SaveJSON(tie.ConfigPath, tie.CurrentState)
	// 		tie.PrintState()
	// 	},
	// }
	// cmds = append(cmds, cmdSetState)

	// var cmdSetCollection = &cobra.Command{
	// 	Use:   "col [collection]",
	// 	Short: "Set current collection",
	// 	Long:  `Set current collection.`,
	// 	Args:  cobra.MinimumNArgs(1),
	// 	Run: func(cmd *cobra.Command, args []string) {
	// 		tie.CurrentState.Collection = args[0]
	// 		tie.SaveJSON(tie.ConfigPath, tie.CurrentState)
	// 		tie.PrintState()
	// 	},
	// }
	// cmds = append(cmds, cmdSetCollection)

	minArgs := 3
	if len(stdin) != 0 {
		minArgs = 2
	}
	var cmdAdd = &cobra.Command{
		Use:   "add [key] [value1] [value2]",
		Short: "Associate two entries.",
		Long:  `Associate two entries.`,
		Args:  cobra.MinimumNArgs(minArgs),
		Run: func(cmd *cobra.Command, args []string) {
			if minArgs == 2 {
				// for _, x := range stdin {
				// a := request.NewAddRequest(x.hash, args[0], args[1])
				// AddHandler(tie.Run(a))
				// }
			} else {
				tie.Add(args[0], args[1], args[2], func(reply *api.AddReply) {
					if !reply.Success {
						fmt.Println("Error while adding!", "Message:", reply.Message)
					}
				})
			}
		},
	}
	cmds = append(cmds, cmdAdd)

	var getFilter string
	var cmdGet = &cobra.Command{
		Use:   "get [key]",
		Short: "Return triple with [key]",
		Long:  "Return triple with [key]",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			tie.Get(args[0], func(reply *api.GetReply) {
				if reply.Success {
					reply.Result.ForEachValue2(func(key, value1, value2 string) {
						// fmt.Printf("%s%20s%20s\n", key, value1, value2)
						fmt.Println(key + "\t" + value1 + "\t" + value2)
					})
				} else {
					fmt.Println("Error getting:", args[0])
					fmt.Println("Message:", reply.Message)

				}
			})
		},
	}
	cmdGet.Flags().StringVarP(&getFilter, "filter", "f", "", "Value1 filter")

	cmds = append(cmds, cmdGet)

	// // ---- Get 2

	// var cmdGet2 = &cobra.Command{
	// 	Use:   "get2 [key] [key2] ... [keyN]",
	// 	Short: "Return tie with [key] or Join multiple keys on their 'associated' value",
	// 	Long:  "Return tie with [key] or Join multiple keys on their 'associated' value",
	// 	Args:  cobra.MinimumNArgs(1),
	// 	Run: func(cmd *cobra.Command, args []string) {
	// 		a := request.NewGet2Request(args)
	// 		if len(args) > 1 {
	// 			a.Filter = tiedb.ASSOCIATED
	// 		}
	// 		if getFilter != "" {
	// 			a.Filter = getFilter
	// 		}
	// 		Get2Handler(tie.Run(a))
	// 	},
	// }
	// cmdGet2.Flags().StringVarP(&getFilter, "filter", "f", "", "Relation filter")

	// cmds = append(cmds, cmdGet2)

	// // ---- End Get 2

	var cmdDel = &cobra.Command{
		Use:   "del [key] [value1] [value2]",
		Short: "Delete tie",
		Long:  `Delete tie`,
		Args:  cobra.MinimumNArgs(3),
		Run: func(cmd *cobra.Command, args []string) {
			tie.Delete(args[0], args[1], args[2], func(reply *api.DeleteReply) {
				if !reply.Success {
					fmt.Println("Error while deleting:", reply.Message)
				}
			})
		},
	}
	cmds = append(cmds, cmdDel)

	// // var cmdFilter = &cobra.Command{
	// // 	Use:   "filter [value] [value2]...",
	// // 	Short: "Filter associations",
	// // 	Long:  `Filter associations`,
	// // 	Args:  cobra.MinimumNArgs(2),
	// // 	Run: func(cmd *cobra.Command, args []string) {
	// // 	},
	// // }
	// // // cmdFilter.fla
	// // // localCmd.Flags().StringVarP(&Source, "source", "s", "", "Source directory to read from")
	// // cmds = append(cmds, cmdFilter)

	// minArgsTag := 1
	// if len(stdin) != 0 {
	// 	minArgsTag = 0
	// }
	// var cmdTag = &cobra.Command{
	// 	Use:   "tag [file|dir] [flags...]",
	// 	Short: "tag current dir",
	// 	Long:  `tag current dir`,
	// 	Args:  cobra.MinimumNArgs(minArgsTag),
	// 	Run: func(cmd *cobra.Command, args []string) {
	// 		options := tie.TagOptions{}
	// 		if minArgsTag == 0 {
	// 			for _, x := range stdin {
	// 				tie.Tag(x.hash, args, options, OldAddHandler)
	// 			}
	// 		} else {
	// 			tie.Tag(args[0], args[1:], options, OldAddHandler)
	// 		}
	// 	},
	// }
	// cmds = append(cmds, cmdTag)

	// var cmdBatch = &cobra.Command{
	// 	Use:   "batch [cmd:key] [cmd:key2] ... [cmd:keyN]",
	// 	Short: "batch requests (probably temp command)",
	// 	Long:  "batch requests (probably temp command)",
	// 	Args:  cobra.MinimumNArgs(1),
	// 	Run: func(cmd *cobra.Command, args []string) {
	// 		b := request.NewBatchRequest()
	// 		for _, x := range args {
	// 			part := strings.Split(x, ":")

	// 			if len(part) < 2 {
	// 				fmt.Println("Need input args in format 'cmd:key', like get:music")
	// 				return
	// 			}

	// 			switch strings.ToLower(part[0]) {
	// 			case "get":
	// 				a := request.NewGetRequest([]string{strings.Join(part[1:], ":")})
	// 				// if len(args) > 1 {
	// 				// 	a.Filter = tiedb.ASSOCIATED
	// 				// }
	// 				if getFilter != "" {
	// 					a.Filter = getFilter
	// 				}
	// 				b.Get = append(b.Get, a)
	// 			}
	// 		}

	// 		BatchHandler(tie.Run(b))
	// 	},
	// }
	// cmdBatch.Flags().StringVarP(&getFilter, "filter", "f", "", "Relation filter")
	// cmds = append(cmds, cmdBatch)

	return cmds
}

// func OldAddHandler(resp json.RawMessage) {
// s := request.ReplyStatus{}
// if err := json.Unmarshal(resp, &s); err == nil {
// 	if tie.CurrentState.Verbose || !s.Success {
// 		fmt.Println(resp)
// 	}
// } else {
// 	fmt.Println("Error unmarshalling response:", err, resp)
// }

// }

// func AddHandler(reply *request.Reply, err error) {
// if err != nil {
// 	fmt.Println("Error handling 'Add' reponse:", err.Error())
// 	fmt.Println("Received:", string(reply.ReplyRawResponse))
// }
// if tie.CurrentState.Verbose {
// 	fmt.Println(string(reply.ReplyRawResponse))
// }
// var result = *reply.DataStatus()
// if !result.Success {
// 	fmt.Println("Success:", result.Success, "Message:", result.Message)
// }
// }

// func GetHandler(reply *request.Reply, err error) {
// if err != nil {
// 	fmt.Println("Error handling 'Get' reponse:", err.Error())
// 	fmt.Println("Received:", string(reply.ReplyRawResponse))
// }
// var result = *reply.DataGet()
// var input tie.TieOutput
// var columns []string
// for _, x := range result {
// 	for i, _ := range x.Value2 {
// 		key := x.Item
// 		value1 := x.Value1[i]
// 		value2 := x.Value2[i]
// 		if outputAsTable {
// 			tie.LoadTieOutput(key, value1, value2, &input, &columns)
// 		} else {
// 			fmt.Println(key + "\t" + value1 + "\t" + value2)
// 		}
// 	}
// }
// if outputAsTable {
// 	table := tie.TieOutputToTable(input, columns)
// 	table.Print(columns)
// }
// }

// func Get2Handler(reply *request.Reply, err error) {
// if err != nil {
// 	fmt.Println("Error handling 'Get' reponse:", err.Error())
// 	fmt.Println("Received:", string(reply.ReplyRawResponse))
// }
// var result = *reply.DataGet2()
// // var input tie.TieOutput
// // var columns []string

// result.ForEach(func(key, value1, value2 string) {
// 	fmt.Println(key + "\t" + value1 + "\t" + value2)
// })

// result["file"].ForEach(func(value1, value2 string) {
// 	fmt.Println(value1 + "\t" + value2)
// })

// // key := x.Item
// // value1 := x.Value1[i]
// // value2 := x.Value2[i]
// // if outputAsTable {
// // 	tie.LoadTieOutput(key, value1, value2, &input, &columns)
// // } else {
// // }
// // }
// // }
// // }
// // if outputAsTable {
// // 	table := tie.TieOutputToTable(input, columns)
// // 	table.Print(columns)
// // }
// }

// func DeleteHandler(reply *request.Reply, err error) {
// if err != nil {
// 	fmt.Println("Error handling 'Delete' reponse:", err.Error())
// 	fmt.Println("Received:", string(reply.ReplyRawResponse))
// }
// var result = *reply.DataStatus()
// fmt.Println("Success:", result.Success, "Message:", result.Message)

// }

// func BatchHandler(reply *request.Reply, err error) {
//
//	if err != nil {
//		fmt.Println("Error handling 'Batch' reponse:", err.Error())
//		fmt.Println("Received:", string(reply.ReplyRawResponse))
//	}
//
// var result = *reply.DataBatch()
// var input tie.TieOutput
// var columns []string
//
//	for _, a := range result.Get {
//		for _, x := range a {
//			for i, _ := range x.Value2 {
//				key := x.Item
//				value1 := x.Value1[i]
//				value2 := x.Value2[i]
//				if outputAsTable {
//					tie.LoadTieOutput(key, value1, value2, &input, &columns)
//				} else {
//					fmt.Println(key + "\t" + value1 + "\t" + value2)
//				}
//			}
//		}
//	}
//
//	if outputAsTable {
//		table := tie.TieOutputToTable(input, columns)
//		table.Print(columns)
//	}
// }
