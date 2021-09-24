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
	insecure   bool
	server     string
)

func main() {
	set := flag.NewFlagSet("", flag.ExitOnError)
	set.BoolVar(&jsonOutput, "json", false, "")
	set.StringVar(&server, "server", "", "")
	set.BoolVar(&insecure, "insecure", false, "Use HTTP instead of HTTPS.")

	if len(os.Args) > 2 {
		set.Parse(os.Args[2:])
	}
	protocol := "https://"
	if insecure {
		protocol = "http://"
	}
	if len(os.Args) > 1 {
		file := os.Args[1]
		var url string
		if server != "" {
			url = protocol + server
		} else {
			url = protocol + "localhost:1162"
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
