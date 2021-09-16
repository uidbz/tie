// put project main.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"git.sr.ht/~uid/tie/io/putlib"
)

var (
	jsonOutput bool
	server     string
)

func main() {
	set := flag.NewFlagSet("", flag.ExitOnError)
	set.BoolVar(&jsonOutput, "json", false, "")
	set.StringVar(&server, "server", "", "")
	if len(os.Args) > 2 {
		set.Parse(os.Args[2:])
	}
	if len(os.Args) > 1 {
		file := os.Args[1]
		var url string
		if server != "" {
			url = "http://" + server
		} else {
			url = "http://localhost:1162"
		}

		pc := putlib.PutConfig{}
		pc.JsonOutput = jsonOutput
		status := putlib.Upload(url, file, pc)
		if status.ErrorMsg != "" {
			fmt.Println(status.ErrorMsg)
			return
		}
		if jsonOutput {
			rawJson, _ := json.Marshal(status)
			fmt.Println(string(rawJson))
		} else {
			fmt.Println(status.LastItem.Hash + "\t" + status.LastItem.Filename)
		}
	} else {
		fmt.Println("Need file/dir to upload")
	}
}
