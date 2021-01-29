package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.sr.ht/~uid/tie-client"
	"github.com/spf13/cobra"
)

func cmdList() []*cobra.Command {
	cmds := []*cobra.Command{}

	var cmdPrint = &cobra.Command{
		Use:   "print [string to print]",
		Short: "Print anything to the screen",
		Long: `print is for printing anything back to the screen.
For many years people have printed back to the screen.`,
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Print: " + strings.Join(args, " "))
		},
	}
	cmds = append(cmds, cmdPrint)

	var cmdEcho = &cobra.Command{
		Use:   "echo [string to echo]",
		Short: "Echo anything to the screen",
		Long: `echo is for echoing anything back.
Echo works a lot like print, except it has a child command.`,
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Echo: " + strings.Join(args, " "))
		},
	}
	cmds = append(cmds, cmdEcho)

	var cmdSetState = &cobra.Command{
		Use:   "set [webservice] [namespace] [collection]",
		Short: "Set current namespace + collection",
		Long:  `Set current namespace + collection.`,
		Args:  cobra.MinimumNArgs(3),
		Run: func(cmd *cobra.Command, args []string) {
			tie.CurrentState.Webservice = args[0]
			tie.CurrentState.Namespace = args[1]
			tie.CurrentState.Collection = args[2]
			SaveJSON(configPath, tie.CurrentState)
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
			SaveJSON(configPath, tie.CurrentState)
			tie.PrintState()
		},
	}
	cmds = append(cmds, cmdSetCollection)

	var cmdAdd = &cobra.Command{
		Use:   "add [key] [value1] [value2]",
		Short: "Associate two entries.",
		Long:  `Associate two entries.`,
		Args:  cobra.MinimumNArgs(3),
		Run: func(cmd *cobra.Command, args []string) {
			a := tie.Association{
				args[0],
				args[1],
				args[2],
			}
			b, _ := json.Marshal(a)
			tie.SendToWebservice("Associate", b, AddHandler)
		},
	}
	cmds = append(cmds, cmdAdd)

	var cmdGet = &cobra.Command{
		Use:   "get [entry1] [relation] [entry2]",
		Short: "Associate two entries.",
		Long:  `Associate two entries.`,
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			a := tie.RequestGet{}
			a.Value = args[0]
			if len(args) > 1 {
				a.Relation = args[1]
			}
			a.MaxAssociations = 0
			b, _ := json.Marshal(a)
			tie.SendToWebservice("Get", b, GetHandler)
		},
	}
	cmds = append(cmds, cmdGet)

	var cmdGroupGet = &cobra.Command{
		Use:   "gget [key]",
		Short: "Associate two entries.",
		Long:  `Associate two entries.`,
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			a := tie.RequestGroupGet{}
			a.Key = args[0]
			if len(args) > 1 {
				a.Value1 = args[1]
			}
			a.MaxAssociations = 0
			b, _ := json.Marshal(a)
			tie.SendToWebservice("GroupGet", b, GetHandler)
		},
	}
	cmds = append(cmds, cmdGroupGet)

	var cmdGroupAdd = &cobra.Command{
		Use:   "gadd [group] [key] [value1] [value2]",
		Short: "Associate tie to group",
		Long:  `Associate tie to group`,
		Args:  cobra.MinimumNArgs(4),
		Run: func(cmd *cobra.Command, args []string) {
			a := tie.GroupAssociation{
				args[0],
				args[1],
				args[2],
				args[3],
			}
			b, _ := json.Marshal(a)
			tie.SendToWebservice("GroupAdd", b, AddHandler)
		},
	}
	cmds = append(cmds, cmdGroupAdd)

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

	var cmdTag = &cobra.Command{
		Use:   "tag [file|dir] [flags...]",
		Short: "tag current dir",
		Long:  `tag current dir`,
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("INPUT", args[0])
			fi, _ := os.Lstat(args[0])
			path, _ := filepath.Abs(args[0])
			if fi.IsDir() {
				ProcessDir(path, hashRoot, args[1:])
			} else {
				ProcessFile(path, fi.Name(), hashRoot, args[1:])
			}

			// var input, _ = os.Getwd()
			// if string(input[len(input)-1]) == "/" {
			// 	input = input[0 : len(input)-1]
			// }

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
			fmt.Println(x.Item + "\t" + x.Relations[i] + "\t" + x.Associations[i])

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
