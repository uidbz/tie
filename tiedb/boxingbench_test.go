package tiedb

import (
	"os"
	"runtime"
	"testing"

	rbt2 "github.com/emirpasic/gods/v2/trees/redblacktree"
)

// assocVal mirrors what an association subtree stores: a disk position, or a
// resident Triple in memory mode. A typed tree can hold this without boxing.
type assocVal struct {
	pos int64
	t   Triple
}

// TestBoxingComparison measures retained heap of ONE association tree holding N
// entries under three representations, isolating the interface{}-boxing cost the
// v2 generics migration would remove. Gated behind TIE_PROF=1.
//
//	go test ./tiedb -run TestBoxingComparison -v   (with TIE_PROF=1)
func TestBoxingComparison(t *testing.T) {
	if os.Getenv("TIE_PROF") == "" {
		t.Skip("set TIE_PROF=1 to run the boxing comparison")
	}
	n := envInt("TIE_PROF_N", 1000000)

	// build returns the constructed tree; measure keeps it alive across the
	// post-build GC+snapshot so the delta reflects RETAINED (live) heap.
	measure := func(name string, build func() any) {
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		tr := build()
		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		d := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		t.Logf("%-28s  heap=%s  (%.1f bytes/entry)", name, bytesH(uint64(d)), float64(d)/float64(n))
		runtime.KeepAlive(tr)
	}

	// v1: interface{} key + interface{} value (current production representation).
	measure("v1 interface{} (disk pos)", func() any {
		tr := NewTreeWith(UniqueAssociationComparator)
		for i := 0; i < n; i++ {
			tr.Put(UniqueAssociation{AssociateTo: uint64(i), Relation: uint64(i % 3)}, int64(i))
		}
		return tr
	})

	// v2: typed key + typed value, no boxing.
	measure("v2 typed[UA,int64]", func() any {
		tr := rbt2.NewWith[UniqueAssociation, int64](func(a, b UniqueAssociation) int {
			if a.AssociateTo != b.AssociateTo {
				if a.AssociateTo < b.AssociateTo {
					return -1
				}
				return 1
			}
			if a.Relation < b.Relation {
				return -1
			}
			if a.Relation > b.Relation {
				return 1
			}
			return 0
		})
		for i := 0; i < n; i++ {
			tr.Put(UniqueAssociation{AssociateTo: uint64(i), Relation: uint64(i % 3)}, int64(i))
		}
		return tr
	})

	// v2 with a struct value (memory-mode Triple), still unboxed.
	measure("v2 typed[UA,assocVal]", func() any {
		tr := rbt2.NewWith[UniqueAssociation, assocVal](func(a, b UniqueAssociation) int {
			if a.AssociateTo != b.AssociateTo {
				if a.AssociateTo < b.AssociateTo {
					return -1
				}
				return 1
			}
			if a.Relation < b.Relation {
				return -1
			}
			if a.Relation > b.Relation {
				return 1
			}
			return 0
		})
		for i := 0; i < n; i++ {
			tr.Put(UniqueAssociation{AssociateTo: uint64(i), Relation: uint64(i % 3)}, assocVal{pos: int64(i)})
		}
		return tr
	})
}
