package main

import (
	"fmt"
	"io"
	"os"

	"github.com/h2non/filetype"
)

func IsImage(file io.Reader) bool {
	head := make([]byte, 261)
	if _, err := file.Read(head); err != nil {
		return false
	}

	return filetype.IsImage(head)
}

func IsImageFromPath(path string) bool {
	file, err := os.Open(path)
	defer file.Close()
	if err != nil {
		fmt.Println("Error opening:", path)
		return false
	}
	return IsImage(file)
}
