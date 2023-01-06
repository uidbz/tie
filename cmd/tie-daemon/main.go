package main

import (
	"flag"
	"os"

	// "bufio"
	"fmt"
	"log"
	"net/http"

	// "os"
	// "strings"
	// "path/filepath"

	// "time"

	"github.com/julienschmidt/httprouter"
	// "github.com/pkg/profile"
	"git.sr.ht/~uid/tie/tiedb"
)

var (
	__VERBOSE bool = true
	__DEBUG   bool = true

	db     *tiedb.Tree
	dbPath string
)

func main() {
	var insecure = flag.Bool("insecure", false, "Use HTTP instead of HTTPS.")
	var certFile = flag.String("tls-cert", "", "Root certificate filename.")
	var keyFile = flag.String("tls-key", "", "Private key filename.")
	var path = flag.String("db-path", "", "Databases path.")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, `Serves a tie database and provides a REST API
-------------------------
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	// defer profile.Start(profile.MemProfile).Stop()
	// defer profile.Start().Stop()
	// start := time.Now()

	db = tiedb.NewDB(true)

	// c := GetCollection(CollectionKey{"main", "cool26"})
	// // c.Associate("a", "b", "c")
	// // fmt.Println(c.GetAssociations())
	// ok, msg := c.Update("a", "b2", "c", "b3")
	// if !ok {
	// 	fmt.Println(msg)
	// }

	// found, assPtr := c.GetAssociations("b2")

	// if found {
	// 	set := c.SetToString("b2", assPtr)
	// 	fmt.Println(*set)
	// }

	// filepath.Walk("/home/johan/apps", db.visit)

	// fmt.Println("--- ", time.Since(start), " ---")

	if *path == "" {
		fmt.Println("Error: Please provide --db-path <path>")
		fmt.Println("Exiting.")
		return
	} else {
		dbPath = *path
	}

	router := httprouter.New()
	routes(router)
	RequestsToAnswer = make(chan *Request, 1000)
	StartRequestAnswerer()

	if *insecure {
		fmt.Println("Listening on http://0.0.0.0:1161\n")
		fmt.Println("API: http://host:1161/Group/Collection/[Add|Associate|Get|InnerJoin]")
		log.Fatal(http.ListenAndServe(":1161", router))
	} else {
		if *certFile == "" || *keyFile == "" {
			fmt.Println("Error: Please provide --tls-cert <file.crt> and --tls-key <file.key> or set --insecure.")
			fmt.Println("Exiting.")
			return
		}
		fmt.Println("Listening on https://0.0.0.0:1161\n")
		fmt.Println("API: https://host:1161/Group/Collection/[Add|Associate|Get|InnerJoin]")
		log.Fatal(http.ListenAndServeTLS(":1161", *certFile, *keyFile, router))
	}
}
