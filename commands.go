package main

import (
	"encoding/json"
	"fmt"

	"git.sr.ht/~uid/tie-client"
	"github.com/spf13/cobra"
)

func cmdList() []*cobra.Command {
	cmds := []*cobra.Command{}

	var cmdSetState = &cobra.Command{
		Use:   "set [webservice] [namespace] [collection]",
		Short: "Set current namespace + collection",
		Long:  `Set current namespace + collection.`,
		Args:  cobra.MinimumNArgs(3),
		Run: func(cmd *cobra.Command, args []string) {
			tie.CurrentState.Webservice = args[0]
			tie.CurrentState.Namespace = args[1]
			tie.CurrentState.Collection = args[2]
			tie.SaveJSON(tie.ConfigPath, tie.CurrentState)
			tie.PrintState()
		},
	}
	cmds = append(cmds, cmdSetState)

	var cmdSetCollection = &cobra.Command{
		Use:   "col [collection]",
		Short: "Set current collection",
		Long:  `Set current collection.`,
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			tie.CurrentState.Collection = args[0]
			tie.SaveJSON(tie.ConfigPath, tie.CurrentState)
			tie.PrintState()
		},
	}
	cmds = append(cmds, cmdSetCollection)

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
				for _, x := range stdin {
					a := tie.Association{
						x.hash,
						args[0],
						args[1],
					}
					b, _ := json.Marshal(a)
					tie.SendToWebservice("Associate", b, AddHandler)
				}
			} else {
				a := tie.Association{
					args[0],
					args[1],
					args[2],
				}
				b, _ := json.Marshal(a)
				tie.SendToWebservice("Associate", b, AddHandler)
			}
		},
	}
	cmds = append(cmds, cmdAdd)

	var cmdGet = &cobra.Command{
		Use:   "get [entry1] [relation]",
		Short: "Associate two entries.",
		Long:  `Associate two entries.`,
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			a := tie.RequestGet{}
			a.Value = args[0]
			if len(args) > 1 {
				a.Relation = args[1:]
			}
			a.MaxAssociations = 0
			b, _ := json.Marshal(a)
			tie.SendToWebservice("Get", b, GetHandler)
		},
	}
	cmds = append(cmds, cmdGet)

	var cmdDel = &cobra.Command{
		Use:   "del [key] [value1] [value2]",
		Short: "Delete tie",
		Long:  `Delete tie`,
		Args:  cobra.MinimumNArgs(3),
		Run: func(cmd *cobra.Command, args []string) {
			a := tie.Association{
				args[0],
				args[1],
				args[2],
			}
			b, _ := json.Marshal(a)
			tie.SendToWebservice("Delete", b, DeleteHandler)
		},
	}
	cmds = append(cmds, cmdDel)

	var cmdFilter = &cobra.Command{
		Use:   "filter [value] [value2]...",
		Short: "Filter associations",
		Long:  `Filter associations`,
		Args:  cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			a := tie.RequestInnerJoin{
				args,
				"associated",
				0,
			}
			b, _ := json.Marshal(a)
			tie.SendToWebservice("InnerJoin", b, GetHandler)
		},
	}
	cmds = append(cmds, cmdFilter)

	minArgsTag := 1
	if len(stdin) != 0 {
		minArgsTag = 0
	}
	var cmdTag = &cobra.Command{
		Use:   "tag [file|dir] [flags...]",
		Short: "tag current dir",
		Long:  `tag current dir`,
		Args:  cobra.MinimumNArgs(minArgsTag),
		Run: func(cmd *cobra.Command, args []string) {
			if minArgsTag == 0 {
				for _, x := range stdin {
					tie.Tag(x.hash, args, AddHandler)
				}
			} else {
				tie.Tag(args[0], args[1:], AddHandler)
			}
		},
	}
	cmds = append(cmds, cmdTag)

	return cmds
}

func AddHandler(resp json.RawMessage) {
	s := tie.Success{}
	if err := json.Unmarshal(resp, &s); err == nil {
		if tie.CurrentState.Verbose || !s.Success {
			fmt.Println(resp)
		}
	} else {
		fmt.Println("Error unmarshalling response:", err, resp)
	}

}

func GetHandler(resp json.RawMessage) {
	var result []tie.ReplyGet
	err := json.Unmarshal(resp, &result)
	if err != nil {
		fmt.Println("Error handling Get reponse:", err)
	}
	for _, x := range result {
		for i, _ := range x.Associations {
			key := x.Item
			value1 := x.Relations[i]
			value2 := x.Associations[i]
			fmt.Println(key + "\t" + value1 + "\t" + value2)
		}
	}
}

func DeleteHandler(resp json.RawMessage) {
	// s := tie.Success{}
	// if err := json.Unmarshal(resp, &s); err == nil {
	// 	if tie.CurrentState.Verbose || !s.Success {
	fmt.Println(string(resp))
	// }
	// } else {
	// fmt.Println("Error unmarshalling response:", err, resp)
	// }

}
