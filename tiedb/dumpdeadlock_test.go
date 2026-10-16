package tiedb

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestForEachTripleConcurrentAddNoDeadlock is the regression test for the
// dump-vs-writer deadlock: lockedTree.ForEach held a shard RLock while its
// callback re-looked-up the same tree (getAssociations), a recursive read
// lock. Any Add of a new subject queued a writer on that shard and wedged
// the dump and every subsequent writer forever.
func TestForEachTripleConcurrentAddNoDeadlock(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"", "dump-race"})

	const seed = 20000
	for i := 0; i < seed; i++ {
		col.Add("subj"+strconv.Itoa(i), "tag", "v"+strconv.Itoa(i%7))
	}

	done := make(chan struct{})
	var dumped int
	go func() {
		defer close(done)
		col.ForEachTriple(func(st StringTriple) {
			dumped++
		})
	}()

	// Keep adding brand-new subjects while the dump runs; before the fix the
	// writer froze after a handful of adds.
	var wg sync.WaitGroup
	added := 0
	var addedMu sync.Mutex
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				select {
				case <-done:
					return
				default:
				}
				col.Add("new"+strconv.Itoa(w)+"-"+strconv.Itoa(i), "tag", "x")
				addedMu.Lock()
				added++
				addedMu.Unlock()
			}
		}(w)
	}

	dumpDone := false
	select {
	case <-done:
		dumpDone = true
	case <-time.After(30 * time.Second):
		t.Fatal("ForEachTriple deadlocked against concurrent Add")
	}
	if !dumpDone {
		t.Fatal("dump did not finish")
	}
	if dumped < seed {
		t.Errorf("dump emitted %d triples, want at least %d", dumped, seed)
	}
	wg.Wait()

	// The collection must still serve writes and reads after the dump.
	col.Add("after-dump", "tag", "still-alive")
	if _, found := col.Get("after-dump", "tag"); !found {
		t.Error("collection wedged after concurrent dump")
	}
}
