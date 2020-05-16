package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"nicecode.rocks/uid/tie-client"
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

	var cmdAdd = &cobra.Command{
		Use:   "add [entry1] [relation] [entry2]",
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
