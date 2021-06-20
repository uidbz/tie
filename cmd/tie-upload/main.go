// put project main.go
package main

import (
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
			url = "http://" + server + "/upload/"
		} else {
			url = "http://localhost:1162/upload/"
		}

		pc := putlib.PutConfig{}
		pc.JsonOutput = jsonOutput
		pc.Upload(url, file)
	} else {
		fmt.Println("Need file/dir to upload")
	}
}
