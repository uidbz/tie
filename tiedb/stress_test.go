package tiedb

import (
	"math/rand"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestConcurrentStress is the CI guard-rail from the review's risk map:
// Add/Delete/Query/Expand/CoTags/Dump/CheckIndex all run concurrently for a
// few seconds under -race. The three lock layers (changeMutex, shard RWMutex,
// AssociationSet locks) plus the writer goroutine have a history of lost
// updates, resurrections and deadlocks; this test is what catches the next
// one. It ends with a CheckIndex that must report a consistent index.
func TestConcurrentStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test skipped in -short")
	}
	for _, disk := range []bool{false, true} {
		t.Run("disk="+strconv.FormatBool(disk), func(t *testing.T) {
			var dir string
			if disk {
				dir = t.TempDir()
			}
			db := NewDB(disk)
			col := db.GetCollection(CollectionKey{dir, "stress"})

			const keys = 500
			key := func(i int) string { return "k" + strconv.Itoa(i%keys) }
			rel := func(i int) string { return "r" + strconv.Itoa(i%5) }
			val := func(i int) string { return "v" + strconv.Itoa(i%7) }

			deadline := time.Now().Add(4 * time.Second)
			var wg sync.WaitGroup
			stop := make(chan struct{})

			// Writers: churn a small key space so adds, deletes and
			// re-adds of the same subjects and reverse keys collide hard.
			for w := 0; w < 4; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					rng := rand.New(rand.NewSource(int64(100 + w)))
					for i := 0; ; i++ {
						select {
						case <-stop:
							return
						default:
						}
						if time.Now().After(deadline) {
							return
						}
						n := rng.Intn(1 << 30)
						switch n % 3 {
						case 0:
							col.Add(key(n), rel(n), val(n))
						case 1:
							col.Delete(key(n), rel(n), val(n))
						default:
							col.SetValues(key(n), rel(n), []string{val(n), val(n + 1)})
						}
					}
				}(w)
			}

			// Readers: every query path, including the ones that nest
			// ForEach (dump, CheckIndex) — the deadlock class from #2.
			for r := 0; r < 4; r++ {
				wg.Add(1)
				go func(r int) {
					defer wg.Done()
					rng := rand.New(rand.NewSource(int64(200 + r)))
					for {
						select {
						case <-stop:
							return
						default:
						}
						if time.Now().After(deadline) {
							return
						}
						n := rng.Intn(1 << 30)
						switch n % 5 {
						case 0:
							col.QueryTags(TagQuery{Include: []string{val(n)}, Reverse: true, Filter: rel(n)})
						case 1:
							col.Get(key(n), rel(n))
						case 2:
							col.ExpandKeys([]string{key(n), key(n + 1)}, "")
						case 3:
							col.CoTags(TagQuery{Include: []string{val(n)}, Reverse: true}, "r0")
						default:
							var n2 int
							col.ForEachTriple(func(StringTriple) { n2++ })
						}
					}
				}(r)
			}

			// CheckIndex concurrently with the churn: it takes
			// changeMutex and walks both indexes while writers queue.
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					if time.Now().After(deadline) {
						return
					}
					col.CheckIndex(IndexCheckOptions{SampleLimit: 5})
					time.Sleep(200 * time.Millisecond)
				}
			}()

			wg.Wait()
			close(stop)

			// Quiesce, then the index must be consistent.
			col.Sync()
			rep := col.CheckIndex(IndexCheckOptions{Deep: disk, SampleLimit: 10})
			if rep.Problems() > 0 {
				t.Errorf("index inconsistent after stress: %+v", rep)
				for i, s := range rep.Samples {
					if i >= 5 {
						break
					}
					t.Logf("  %s: %q %q %q @%d", s.Kind, s.Key, s.Value1, s.Value2, s.Position)
				}
			}
		})
	}
}
