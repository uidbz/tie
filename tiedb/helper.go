package tiedb

import (
	"sync/atomic"

	"fmt"
	"math/rand"
	"time"
)

var __DEBUG bool = true
var __VERBOSE bool = true

func Debug(value string) {
	if __DEBUG {
		fmt.Println(value)
	}
}

func Info(value string) {
	if __VERBOSE || __DEBUG {
		fmt.Println(value)
	}
}

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// Used for testing worst-case scenario
func (t *Collection) WriteRandomDB(filename string) {
	fmt.Println("Writing random database")
	rand.Seed(time.Now().UnixNano())
	for i := 0; i < 100000; i++ {
		t.insert(randSeq(100))
	}
	fmt.Println("Done writing.")
}
func randSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func (ic *Collection) GetTotalEntries() uint64 {
	return ic.totalEntries
}

func (ic *Collection) nextID() uint64 {
	return atomic.AddUint64(&ic.totalEntries, 1)
}

