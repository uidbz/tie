package main

import (
	"errors"
	"fmt"
	"strings"

	"git.sr.ht/~uid/tie/tiedb"

	"git.sr.ht/~uid/tie/io/putlib"

	"git.sr.ht/~uid/conf"

	"git.sr.ht/~uid/tie/api"
	"git.sr.ht/~uid/tie/client"

	"github.com/urfave/cli/v2"
	// "github.com/spf13/cobra"
)

func cmdAdd() *cli.Command {
	return &cli.Command{
		Name:    "add",
		Aliases: []string{"a"},
		Usage:   "Add a triple to the database: add [key] [value1] [value2]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "gallery-name", Aliases: []string{"g"}},
			&cli.StringFlag{Name: "tags", Aliases: []string{"t"}},
		},

		Action: func(cCtx *cli.Context) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			if cCtx.Args().Len() < 3 {
				return errors.New("Need 3 args: Key, Value1, Value2")
			}
			args := cCtx.Args().Slice()
			var err error = nil

			tie.Add(args[0], args[1], args[2], func(reply *api.AddReply) {
				if !reply.Success {
					err = errors.New("Add Error: " + reply.Message)
				}
			})

			return err
		},
	}

}

func cmdGet() *cli.Command {
	return &cli.Command{
		Name:    "get",
		Aliases: []string{"g"},
		Usage:   "Get triples: get [key] (or multiple keys for intersection of reverse value. Use -[key] to exclude instead of joining)",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "reverse", Aliases: []string{"r"}},
			&cli.IntFlag{Name: "limit", Aliases: []string{"l"}, Value: 1000},
			&cli.IntFlag{Name: "offset", Aliases: []string{"o"}, Value: 0},
			&cli.StringFlag{Name: "sortby", Aliases: []string{"s"}},
			&cli.StringFlag{Name: "filter", Aliases: []string{"f"}},
		},
		Action: func(ctx *cli.Context) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			if ctx.Args().Len() < 1 {
				return errors.New("Need 1 arg: Key")
			}
			args := ctx.Args().Slice()
			var reverse bool = false
			var err error = nil
			if len(args) > 1 {
				reverse = true
			} else {
				reverse = ctx.Bool("reverse")
			}
			intersecting := make([]api.Transform, 0)
			excluding := make([]api.Transform, 0)
			for i := 1; i < len(ctx.Args().Slice()); i++ {
				if strings.HasPrefix(args[i], "-") {
					excluding = append(excluding, api.Transform{Key: strings.TrimPrefix(args[i], "-"), Reverse: true})
				} else {
					intersecting = append(intersecting, api.Transform{Key: args[i], Reverse: true})
				}
			}
			o := api.GetOptions{
				Reverse:   reverse,
				Intersect: intersecting,
				Exclude:   excluding,
				Filter:    ctx.String("filter"),
				Sort: tiedb.SortOptions{
					Offset: ctx.Int("offset"),
					Limit:  ctx.Int("limit"),
					SortBy: ctx.String("sortby"),
				},
			}
			tie.Get(args[0], o, func(reply *api.GetReply) {
				if reply.Success {
					reply.Result.ForEachValue2(func(key, value1, value2 string) {
						fmt.Println(key + "\t" + value1 + "\t" + value2)
					})
				} else {
					err = errors.New("Get Error: " + reply.Message)
				}
			})

			return err
		},
	}
}

func cmdDel() *cli.Command {
	return &cli.Command{
		Name:    "del",
		Aliases: []string{"d"},
		Usage:   "Delete a triple: del [key] [value1] [value2]",
		Action: func(cCtx *cli.Context) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			if cCtx.Args().Len() < 3 {
				return errors.New("Need 3 args: Key, Value1, Value2")
			}
			args := cCtx.Args().Slice()
			var err error = nil
			handleReply := func(reply *api.DeleteReply) {
				if !reply.Success {
					err = errors.New("Delete Error: " + reply.Message)
				}
			}
			deleteBatch := func(reply *api.GetReply) {
				if reply.Success {
					b := tie.NewBatch()
					reply.Result.ForEachValue2(func(key, val1, val2 string) {
						b.Delete(key, val1, val2)
					})
					tie.Batch(b, func(reply *api.BatchReply) {
						for _, d := range reply.DeleteReplys {
							if !d.Success {
								err = errors.New("First Delete Error: " + reply.Message)
								break
							}
						}
					})
				} else {
					fmt.Println("Error:", reply.Message)
				}
			}
			switch true {
			case args[1] == "*" && args[2] == "*":
				o := api.GetOptions{
					Filter: args[1],
					Sort:   tiedb.SortOptions{Limit: -1},
				}
				tie.Get(args[0], o, deleteBatch)

			case args[1] != "*" && args[2] == "*":
				o := api.GetOptions{
					Filter: args[1],
					Sort:   tiedb.SortOptions{Limit: -1},
				}
				tie.Get(args[0], o, deleteBatch)

			case args[1] == "*" && args[2] != "*":
				fmt.Println("Function to delete specific value2 from all value1's is not implemented, because it is most likely a typo.")
				fmt.Println("Did you mean: tie delete", args[0], args[2], "* (delete all value2's from specific key + value1 pair?)")

			default:
				tie.Delete(args[0], args[1], args[2], handleReply)

			}

			return err
		},
	}
}

func cmdConf() *cli.Command {
	return &cli.Command{
		Name:    "conf",
		Aliases: []string{"c"},
		Usage:   "conf",
		Subcommands: []*cli.Command{
			{
				Name:  "create",
				Usage: "Create new default config file: conf create [name]",
				Action: func(cCtx *cli.Context) error {
					var name string
					if cCtx.Args().Len() == 0 {
						name = "config"
					} else {
						name = cCtx.Args().First()
					}
					err := client.SaveConfig(name, client.DefaultConfig())
					if err != nil {
						return err
					}
					path, err := conf.PathUserConfigDir("tie", name)
					fmt.Println("Config created here:", path)

					return err
				},
			},
			// {
			// 	Name:  "remove",
			// 	Usage: "remove an existing template",
			// 	Action: func(cCtx *cli.Context) error {
			// 		fmt.Println("removed task template: ", cCtx.Args().First())
			// 		return nil
			// 	},
			// },
		},
	}
}

func ImportImage() *cli.Command {
	return &cli.Command{
		Name:    "image",
		Aliases: []string{"img"},
		Usage:   "Upload and tag an image",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "gallery", Aliases: []string{"g"}, Usage: "Tag with gallery name"},
			&cli.StringSliceFlag{Name: "tags", Aliases: []string{"t"}},
			&cli.StringSliceFlag{Name: "host"},
		},
		// Subcommands: []*cli.Command{
		// 	{
		// 		Name:  "tags",
		// 		Aliases: []string{"t"},
		// 		Usage: "tags to add to the importet item(s)",
		// 		Action: func(cCtx *cli.Context) error {
		// 			fmt.Println("new task template: ", cCtx.Args().First())
		// 			return nil
		// 		},
		// 	},
		Action: func(ctx *cli.Context) error {
			if tie == nil {
				return errors.New("Error: Config not loaded")
			}
			for _, file := range ctx.Args().Slice() {
				if IsImageFromPath(file) {
					var hosts []string
					fmt.Println("host '" + ctx.String("host") + "'")
					if len(ctx.StringSlice("host")) == 0 {
						hosts = tie.Config.DefaultFileHosts
						fmt.Println("here", tie.Config.DefaultFileHosts)
					} else {
						hosts = ctx.StringSlice("host")
					}
					fmt.Println("Uploading to", hosts)
					for _, h := range hosts {
						status := putlib.Upload(tie.Config.FileHosts[h], file, putlib.PutConfig{})
						for _, x := range status.UploadedItems {
							if x.ErrorMsg == "" {
								fmt.Printf("%v %v\n", x.Hash, x.Filename)
								info := client.EssentialTagInfo(x.Hash, file, x.MediaType, client.TieImageFile, ctx.StringSlice("tags"))
								info.Image.GalleryName = ctx.String("gallery")
								client.Tag(tie, info)
							} else {
								fmt.Printf("Error uploading: %v\n%v\n", x.Filename, x.ErrorMsg)
							}
						}
					}
				}
			}
			return nil
		},
	}
}

func cmdImport() *cli.Command {
	return &cli.Command{
		Name:    "import",
		Aliases: []string{"i"},
		Usage:   "import files to tie-fileserver and tag them",
		Subcommands: []*cli.Command{
			ImportImage(),
			{
				Name:    "video",
				Aliases: []string{"c"},
				Usage:   "complete a task on the list",
				Action: func(cCtx *cli.Context) error {
					fmt.Println("completed task: ", cCtx.Args().First())
					return nil
				},
			},
		},
	}
}

// func cmdList() []*cobra.Command {
// 	cmds := []*cobra.Command{}

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

// minArgs := 3
// if len(stdin) != 0 {
// 	minArgs = 2
// }
// var cmdAdd = &cobra.Command{
// 	Use:   "add [key] [value1] [value2]",
// 	Short: "Associate two entries.",
// 	Long:  `Associate two entries.`,
// 	Args:  cobra.MinimumNArgs(minArgs),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		if minArgs == 2 {
// 			// for _, x := range stdin {
// 			// a := request.NewAddRequest(x.hash, args[0], args[1])
// 			// AddHandler(tie.Run(a))
// 			// }
// 		} else {
// 			tie.Add(args[0], args[1], args[2], func(reply *api.AddReply) {
// 				if !reply.Success {
// 					fmt.Println("Error while adding!", "Message:", reply.Message)
// 				}
// 			})
// 		}
// 	},
// }
// cmds = append(cmds, cmdAdd)

// var getFilter string
// var cmdGet = &cobra.Command{
// 	Use:   "get [key]",
// 	Short: "Return triple with [key]",
// 	Long:  "Return triple with [key]",
// 	Args:  cobra.MinimumNArgs(1),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		tie.Get(args[0], func(reply *api.GetReply) {
// 			if reply.Success {
// 				reply.Result.ForEachValue2(func(key, value1, value2 string) {
// 					// fmt.Printf("%s%20s%20s\n", key, value1, value2)
// 					fmt.Println(key + "\t" + value1 + "\t" + value2)
// 				})
// 			} else {
// 				fmt.Println("Error getting:", args[0])
// 				fmt.Println("Message:", reply.Message)

// 			}
// 		})
// 	},
// }
// cmdGet.Flags().StringVarP(&getFilter, "filter", "f", "", "Value1 filter")

// cmds = append(cmds, cmdGet)

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

// var cmdDel = &cobra.Command{
// 	Use:   "del [key] [value1] [value2]",
// 	Short: "Delete tie",
// 	Long:  `Delete tie`,
// 	Args:  cobra.MinimumNArgs(3),
// 	Run: func(cmd *cobra.Command, args []string) {
// 		tie.Delete(args[0], args[1], args[2], func(reply *api.DeleteReply) {
// 			if !reply.Success {
// 				fmt.Println("Error while deleting:", reply.Message)
// 			}
// 		})
// 	},
// }
// cmds = append(cmds, cmdDel)

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

// return cmds
// }

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
