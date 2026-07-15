// get project main.go
package main

import (
	"flag"
	"fmt"
	"os"

	"git.sr.ht/~uid/tie/io/getlib"
)

var (
	server   string
	insecure bool
)

func main() {
	set := flag.NewFlagSet("", flag.ExitOnError)
	set.StringVar(&server, "server", "", "")
	set.BoolVar(&insecure, "insecure", false, "Use HTTP instead of HTTPS.")

	if len(os.Args) > 3 {
		set.Parse(os.Args[3:])
	}
	protocol := "https://"
	if insecure {
		protocol = "http://"
	}
	if len(os.Args) > 2 {
		source := os.Args[1]
		dest := os.Args[2]
		if dest == "" {
			fmt.Println("Missing destination: tie-download <source-hash> <dest-file>")
		}
		var url string
		if server != "" {
			url = protocol + server
		} else {
			url = protocol + "localhost:1162"
		}
		err := getlib.DownloadFile(nil, url, source, dest)
		if err != nil {
			fmt.Println(err.Error())
		}
	} else {
		fmt.Println("Need file/dir to download")
	}
}
