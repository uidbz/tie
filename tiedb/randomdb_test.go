package tiedb

import (
	"math/rand"
	"testing"
)

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// randSeq returns a random string of length n drawn from letters, using the
// caller's local rng so the sequence is reproducible per run.
func randSeq(rng *rand.Rand, n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rng.Intn(len(letters))]
	}
	return string(b)
}

// TestWriteRandomDB exercises the worst-case insert path: many unique,
// maximally-long random values (100 bytes = several tree levels each, with no
// shared prefixes to dedupe). It replaces the old fire-and-forget
// Collection.WriteRandomDB helper with real assertions:
//   - every inserted value is retrievable afterward,
//   - re-inserting the same values adds no new entries (prefix dedup holds),
//   - the total entry count is deterministic for a fixed seed.
func TestWriteRandomDB(t *testing.T) {
	const values = 2000
	const valueLen = 100

	// Two runs with the same seed must produce identical entry counts.
	counts := make([]uint64, 2)
	for run := 0; run < 2; run++ {
		rng := rand.New(rand.NewSource(1))
		db := NewDB(false)
		col := db.GetCollection(CollectionKey{"mem", "randomtest"})

		inserted := make([]string, values)
		for i := range inserted {
			v := randSeq(rng, valueLen)
			inserted[i] = v
			col.insert(v)
		}

		// Every inserted value must be findable.
		for _, v := range inserted {
			if _, _, found := col.getEntryFromString(v); !found {
				t.Fatalf("run %d: inserted value not found: %q", run, v)
			}
		}

		// A value that was never inserted must not be found.
		if _, _, found := col.getEntryFromString(randSeq(rng, valueLen)); found {
			t.Errorf("run %d: unrelated value unexpectedly found", run)
		}

		// Re-inserting the same values is pure dedup: no new entries.
		before := col.GetTotalEntries()
		for _, v := range inserted {
			col.insert(v)
		}
		if after := col.GetTotalEntries(); after != before {
			t.Errorf("run %d: re-insert grew entries %d -> %d, want no change", run, before, after)
		}

		counts[run] = col.GetTotalEntries()
		if counts[run] == 0 {
			t.Fatalf("run %d: no entries created", run)
		}
	}

	if counts[0] != counts[1] {
		t.Errorf("entry count not deterministic across seeded runs: %d vs %d", counts[0], counts[1])
	}
}
