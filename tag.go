package main

import (
	"encoding/json"
	"fmt"

	"git.sr.ht/~uid/metalib"
	"git.sr.ht/~uid/putlib"
)

func Tag(path string, tags []string) {
	putlib.JsonOutput = true
	url := "http://localhost:1162/upload/"
	output, _ := putlib.Upload(url, key, path)
	if output != "" {
		info := metalib.Info{}
		if err := json.Unmarshal([]byte(output), &info); err != nil {
			fmt.Println("Unmashal error:", err.Error())
		} else {
			uid := metalib.HashFunction + "/" + info.MediaType + "/" + info.Hash
			for _, x := range tags {
				TieAssociate(uid, "tag", x)
				fmt.Println(uid, "tag", x)
			}
		}
	}
}
