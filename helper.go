package main

import (
	"fmt"
	"os"
)

func PrintState() {
	if verbose {
		fmt.Println("Using host:", state.Webservice)
		fmt.Println("Current namespace/collection is " + state.Namespace + "/" + state.Collection)
	}
}

func Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return true, err
}
