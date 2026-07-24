package tiedb

import (
	"log/slog"
	"sync/atomic"

	"fmt"
	"math/rand"
	"time"
)

// Debug and Info are thin wrappers kept for existing call sites; they forward
// to the process slog logger (which tie-daemon/tie-filehost point at a log file
// plus pretty stderr). Diagnostics stay off stdout — stdout is data.
func Debug(value string) { slog.Debug(value) }

func Info(value string) { slog.Info(value) }

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

