// get project main.go
package main

import (
	"flag"
	"fmt"
	"os"

	"git.sr.ht/~uid/tie/io/getlib"
)

var (
	server string
)

func main() {
	set := flag.NewFlagSet("", flag.ExitOnError)
	set.StringVar(&server, "server", "", "")
	if len(os.Args) > 3 {
		set.Parse(os.Args[3:])
	}
	if len(os.Args) > 2 {
		source := os.Args[1]
		dest := os.Args[2]
		var url string
		if server != "" {
			url = "http://" + server
		} else {
			url = "http://localhost:1162"
		}
		err := getlib.DownloadFile(url, source, dest)
		if err != nil {
			fmt.Println(err.Error())
		}
	} else {
		fmt.Println("Need file/dir to download")
	}
}
