package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~uid/tie-client"
)

func CreateTieFilesystem(path string) {
	parts := strings.Split(path, "/")
	currentPath := "/"
	prev := ""
	for i, x := range parts {
		if i == 0 {
			continue
		}

		prev = currentPath
		if i == 1 {
			currentPath += x
		} else {
			currentPath += "/" + x
		}

		if i != len(parts)-1 {
			fmt.Println("prev", prev)
			TieAssociate(prev, "directory", currentPath)
		}
	}
	fmt.Println("associate", path)
	TieAssociate(prev, "file", path)
}

func TieAssociate(Entry1, Relation, Entry2 string) {
	// type Association struct {
	// 	Key    string
	// 	Value1 string
	// 	Value2 string
	// }
	a := tie.Association{
		Key:    Entry1,
		Value1: Relation,
		Value2: Entry2,
	}
	b, _ := json.Marshal(a)
	tie.SendToWebservice("Associate", b, AddHandler)

}
