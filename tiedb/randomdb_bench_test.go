package tiedb

import (
	"math/rand"
	"testing"
)

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// randSeq returns a random string of length n drawn from letters, using the
// caller's local rng so the sequence is reproducible per benchmark run.
func randSeq(rng *rand.Rand, n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rng.Intn(len(letters))]
	}
	return string(b)
}

// BenchmarkWriteRandomDB measures the worst case: inserting many unique,
// maximally-long random values (no shared prefixes to dedupe). This is the
// former Collection.WriteRandomDB helper, moved here where it belongs — a seeded
// local *rand.Rand keeps it reproducible without the deprecated global rand.Seed.
func BenchmarkWriteRandomDB(b *testing.B) {
	const values = 100000
	const valueLen = 100
	for i := 0; i < b.N; i++ {
		rng := rand.New(rand.NewSource(1))
		db := NewDB(false)
		col := db.GetCollection(CollectionKey{"mem", "randombench"})
		for j := 0; j < values; j++ {
			col.insert(randSeq(rng, valueLen))
		}
	}
}
