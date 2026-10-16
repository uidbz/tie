package tiedb

import (
	"strconv"
	"sync"
	"testing"
)

// TestSetValuesAtomicUnderConcurrency pins #24: Set is "make this relation
// hold exactly these values". When SetValues took and released changeMutex
// per step, two concurrent Sets could interleave into the UNION of both
// value sets. (A reader can still observe an intermediate state — readers
// never took changeMutex — but the relation must never end up holding a mix
// written by two different Sets.)
func TestSetValuesAtomicUnderConcurrency(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "atomic-set"})

	const rounds = 200
	var wg sync.WaitGroup

	setA := []string{"a1", "a2", "a3"}
	setB := []string{"b1", "b2"}

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			col.SetValues("k", "rel", setA)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			col.SetValues("k", "rel", setB)
		}
	}()
	wg.Wait()
	row, found := col.Get("k", "rel")
	if !found {
		t.Fatal("relation missing after Sets")
	}
	got := map[string]bool{}
	row["k"]["rel"].ForEach(func(v string) { got[v] = true })
	isA := len(got) == 3 && got["a1"] && got["a2"] && got["a3"]
	isB := len(got) == 2 && got["b1"] && got["b2"]
	if !isA && !isB {
		t.Errorf("final relation = %v, want exactly one of the two written sets (union leak)", keysOf(got))
	}
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestUpdateAtomic: concurrent Update of the same triple must never leave
// the relation holding both the old and the new value.
func TestUpdateAtomic(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"mem", "atomic-update"})
	col.Add("k", "rel", "v0")

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				col.Update("k", "rel", "v"+strconv.Itoa(i%2), "v"+strconv.Itoa((i+1)%2))
			}
		}(w)
	}
	wg.Wait()

	row, found := col.Get("k", "rel")
	if !found {
		t.Fatal("relation gone")
	}
	n := 0
	row["k"]["rel"].ForEach(func(string) { n++ })
	if n != 1 {
		t.Errorf("relation holds %d values, want exactly 1", n)
	}
}
