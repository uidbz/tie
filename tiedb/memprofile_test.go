package tiedb

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"testing"
)

// memStats snapshots the live heap after forcing a GC so the numbers reflect
// retained (not garbage) memory.
func memStats() runtime.MemStats {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m
}

// hexKey fabricates a 64-char hex content-address-like key for index i.
func hexKey(i int) string {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, 64)
	v := uint64(i)
	for j := 63; j >= 0; j-- {
		b[j] = hexdigits[v&0xf]
		v >>= 4
	}
	return string(b)
}

// countAssocNodes walks the outer entry tree and sums the sizes of the inner
// association sub-trees, returning (outer entries, total inner nodes).
func countAssocNodes(t *lockedTree[uint64, *AssociationSet]) (outer, inner int) {
	if t == nil {
		return 0, 0
	}
	t.ForEach(func(_ uint64, sub *AssociationSet) {
		outer++
		inner += sub.Size() // inline-aware: counts un-promoted lazy trees too
	})
	return outer, inner
}

// TestAssociationMemoryProfile bulk-loads associations and reports retained heap
// so the boxing/node overhead of the association index can be measured before
// deciding whether to replace the gods rbt with a typed tree.
//
// Tunables (env vars):
//
//	TIE_PROF_N       number of (key,relation,value) triples to load (default 200000)
//	TIE_PROF_MODE    "disk" (default) or "mem"
//	TIE_PROF_REVERSE "all" (default), "none", or a single relation name to allowlist
//	TIE_PROF_RELS    number of distinct relations to spread across (default 3)
//	TIE_PROF_HEAP    if set, write a heap pprof profile to this path
//
// Run e.g.:
//
//	go test ./tiedb -run TestAssociationMemoryProfile -v
//	TIE_PROF_N=1000000 TIE_PROF_MODE=disk TIE_PROF_HEAP=/tmp/assoc.heap \
//	  go test ./tiedb -run TestAssociationMemoryProfile -v -timeout 30m
func TestAssociationMemoryProfile(t *testing.T) {
	if os.Getenv("TIE_PROF") == "" {
		t.Skip("set TIE_PROF=1 to run the association memory profiler")
	}

	n := envInt("TIE_PROF_N", 200000)
	rels := envInt("TIE_PROF_RELS", 3)
	if rels < 1 {
		rels = 1
	}
	mode := os.Getenv("TIE_PROF_MODE")
	if mode == "" {
		mode = "disk"
	}
	writeToDisk := mode != "mem"
	reverse := os.Getenv("TIE_PROF_REVERSE")
	if reverse == "" {
		reverse = "all"
	}

	relNames := make([]string, rels)
	for i := range relNames {
		relNames[i] = "rel" + strconv.Itoa(i)
	}

	db := NewDB(writeToDisk)
	switch reverse {
	case "all":
		// default: index every relation in reverse
	case "none":
		db.SetDefaultReverseRelations([]string{}) // empty allowlist -> no reverse
	default:
		db.SetDefaultReverseRelations([]string{reverse})
	}

	dbDir := "mem"
	if writeToDisk {
		dbDir = t.TempDir()
	}
	col := db.GetCollection(CollectionKey{dbDir, "profile"})

	before := memStats()

	for i := 0; i < n; i++ {
		key := hexKey(i)
		rel := relNames[i%rels]
		col.Add(key, rel, "value"+strconv.Itoa(i))
	}
	col.Sync()

	after := memStats()

	var outer, inner, rOuter, rInner int
	for i := range col.levels {
		o, in := countAssocNodes(col.levels[i].associations)
		ro, rin := countAssocNodes(col.levels[i].reverseAssociations)
		outer += o
		inner += in
		rOuter += ro
		rInner += rin
	}

	heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	objDelta := int64(after.HeapObjects) - int64(before.HeapObjects)

	t.Logf("=== association memory profile ===")
	t.Logf("mode=%s reverse=%s N=%d relations=%d", mode, reverse, n, rels)
	t.Logf("forward index:  outer entries=%d  inner assoc nodes=%d", outer, inner)
	t.Logf("reverse index:  outer entries=%d  inner assoc nodes=%d", rOuter, rInner)
	t.Logf("HeapAlloc:   %s -> %s  (delta %s)",
		bytesH(before.HeapAlloc), bytesH(after.HeapAlloc), bytesH(uint64(heapDelta)))
	t.Logf("HeapObjects: %d -> %d  (delta %d)", before.HeapObjects, after.HeapObjects, objDelta)
	t.Logf("Sys (total from OS): %s", bytesH(after.Sys))
	if n > 0 {
		t.Logf("per triple:  %.1f bytes/triple  %.2f heap objects/triple",
			float64(heapDelta)/float64(n), float64(objDelta)/float64(n))
	}
	totalNodes := inner + rInner
	if totalNodes > 0 {
		t.Logf("per assoc node: %.1f bytes/node  (over %d fwd+rev nodes)",
			float64(heapDelta)/float64(totalNodes), totalNodes)
	}

	if p := os.Getenv("TIE_PROF_HEAP"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create heap profile: %v", err)
		}
		defer f.Close()
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatalf("write heap profile: %v", err)
		}
		t.Logf("heap profile written to %s (view: go tool pprof -inuse_space %s)", p, p)
	}

	// Keep col alive across the measurement so the trees aren't collected early.
	runtime.KeepAlive(col)
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func bytesH(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for x := b / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
