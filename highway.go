package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/minio/highwayhash"
)

type Info struct {
	OriginalPath string
}

func AddressOf(key []byte, file string) (uint64, error) { // function to compute address based on content
	fsocket, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer fsocket.Close()

	hash, err := highwayhash.New64(key)
	if err != nil {
		return 0, err
	}

	_, err = io.Copy(hash, fsocket)
	return hash.Sum64(), err
}

func ProcessFile(absPath string, filename string, destRoot string, tags []string) {
	address, err := AddressOf(key, absPath)
	if err != nil {
		fmt.Printf("Failed to read file %s: %v", filename, err) // add error handling
		// return
	}
	// lookupMap[address] = filename
	dest := make([]byte, 8)
	binary.LittleEndian.PutUint64(dest, address)
	h := hex.EncodeToString(dest)
	// fmt.Println(h)
	// path := destRoot
	path := "./"
	for i := 0; i < 14; i = i + 2 {
		path += h[i:i+2] + "/"
	}
	os.MkdirAll(destRoot+path[0:len(path)-3], 0755)
	absDest := destRoot + path[0:len(path)-1]
	FileCopy(absPath, absDest)
	info := Info{absPath}
	jsonInfo, _ := json.Marshal(info)
	fmt.Println("AbsDest", absDest)
	err = ioutil.WriteFile(absDest+".json", jsonInfo, 0644)
	if err != nil {
		panic("Could not write json info file!")
	}
	fmt.Println(absPath)
	CreateTieFilesystem(absPath)
	finalDest := path[0 : len(path)-1]
	TieAssociate(absPath, "data", path[0:len(path)-1])
	// fmt.Println(file.Name()+": ", address, " - ", len(string(dest)), dest, len(strconv.FormatUint(address, 16)))
	fmt.Println(finalDest)
	for _, x := range tags {
		TieAssociate(finalDest, "tag", x)
		// b, _ := json.Marshal(a)
		// tie.SendToWebservice("Delete", b, DeleteHandler)
	}
	errTypeTag := TypeSpecificTags(absDest, finalDest)
	if errTypeTag != nil {
		fmt.Println("Error (Type Tagging):", errTypeTag.Error())
	}
}

func ProcessDir(input string, destRoot string, tags []string) {
	dir, err := ioutil.ReadDir(input)
	if err != nil {
		fmt.Printf("Failed to read current directory: %v", err) // add error handling
		return
	}

	// lookupMap := make(map[uint64]string, len(dir))
	for _, file := range dir {
		if file.IsDir() {
			continue // skip sub-directroies in our example
		}
		ProcessFile(input+"/"+file.Name(), file.Name(), destRoot, tags)
	}
}
