//go:build ignore

// Command gotable round-trips a table through the *Go* client so the Python
// table tests can check cross-client compatibility for real instead of assuming
// it (both clients hand-mirror the same encoding, and consumers are promised the
// two interoperate). It is a test fixture, not part of any build: the ignore tag
// keeps it out of ./..., and `go run` on an explicit file path bypasses the tag.
//
//	go run gotable.go write <uid>   # reads {"headers":…,"rows":…} on stdin, prints the uid
//	go run gotable.go read <uid>    # prints {"headers":…,"rows":…}
//
// headers is a flat list of labels or a list of header rows, matching the union
// insert_table accepts; read always reports header rows, so header depth is
// visible to the caller. Both use client.TestingConfig(), the same
// namespace/collection/port the Go client tests use, which is what makes a table
// written here visible to a Python client configured to match.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/uidbz/tie/client"
)

type table struct {
	Headers json.RawMessage `json:"headers"`
	Rows    [][]string      `json:"rows"`
}

func main() {
	log.SetFlags(0)
	if len(os.Args) != 3 {
		log.Fatal("usage: gotable <write|read> <uid>")
	}
	mode, uid := os.Args[1], os.Args[2]
	tie := client.NewTieClient(client.TestingConfig())

	switch mode {
	case "write":
		var in table
		if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
			log.Fatalf("decoding stdin: %v", err)
		}
		var flat []string
		if err := json.Unmarshal(in.Headers, &flat); err == nil {
			uid, err = tie.InsertTable(uid, flat, in.Rows)
			if err != nil {
				log.Fatalf("InsertTable: %v", err)
			}
			fmt.Println(uid)
			return
		}
		var headerRows [][]string
		if err := json.Unmarshal(in.Headers, &headerRows); err != nil {
			log.Fatalf("headers must be a list of strings or a list of header rows: %v", err)
		}
		uid, err := tie.InsertTableLevels(uid, headerRows, in.Rows)
		if err != nil {
			log.Fatalf("InsertTableLevels: %v", err)
		}
		fmt.Println(uid)

	case "read":
		headerRows, rows, err := tie.ReadTableLevels(uid)
		if err != nil {
			log.Fatalf("ReadTableLevels: %v", err)
		}
		out, err := json.Marshal(table{Headers: mustMarshal(headerRows), Rows: rows})
		if err != nil {
			log.Fatalf("encoding output: %v", err)
		}
		fmt.Println(string(out))

	default:
		log.Fatalf("unknown mode %q; want write or read", mode)
	}
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		log.Fatalf("encoding headers: %v", err)
	}
	return b
}
