package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
)

func LoadJSON(inputFile string, dest interface{}) bool {
	file, err := os.Open(inputFile)

	if err != nil {
		log.Fatal(err)
		return false
	} else {
		input, err2 := ioutil.ReadAll(file)
		json.Unmarshal(input, &dest)
		if err2 != nil {
			log.Fatal(err2)
			return false
		} else {
			if verbose {
				log.Println("Loading succeeded: " + inputFile)
			}
			return true
		}
	}
}

func SaveJSON(outputFile string, source interface{}) bool {
	b, _ := json.Marshal(source)
	err := ioutil.WriteFile(outputFile, b, 0766)
	if err != nil {
		log.Fatal(err)
		return false
	} else {
		return true
	}
}
