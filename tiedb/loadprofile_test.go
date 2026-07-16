package tiedb

import (
	"os"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
)

// TestLoadTimeProfile times loading an existing on-disk .tie database and
// optionally writes a CPU profile of the load path.
//
// Tunables (env vars):
//
//	TIE_LOAD_DB    full path to an existing .tie file (required to run)
//	TIE_LOAD_CPU   if set, write a CPU pprof profile to this path
//	TIE_LOAD_HEAP  if set, write a heap pprof profile to this path after load
//
// Run e.g.:
//
//	TIE_LOAD_DB=/home/johan/db/db1.tie TIE_LOAD_CPU=/tmp/load.cpu \
//	  go test ./tiedb -run TestLoadTimeProfile -v -timeout 60m
func TestLoadTimeProfile(t *testing.T) {
	path := os.Getenv("TIE_LOAD_DB")
	if path == "" {
		t.Skip("set TIE_LOAD_DB=/path/to/db.tie to run the load-time profiler")
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	// Split /dir/name.tie into (dir, name) the way initialize expects.
	dir, base := splitTiePath(path)

	if p := os.Getenv("TIE_LOAD_CPU"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create cpu profile: %v", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatalf("start cpu profile: %v", err)
		}
		defer pprof.StopCPUProfile()
		t.Logf("cpu profile -> %s (view: go tool pprof %s)", p, p)
	}

	db := NewDB(true)

	start := time.Now()
	col := db.GetCollection(CollectionKey{dir, base})
	elapsed := time.Since(start)

	t.Logf("=== load-time profile ===")
	t.Logf("file: %s (%.2f GiB)", path, float64(fi.Size())/(1024*1024*1024))
	t.Logf("load wall time: %s", elapsed)
	t.Logf("total entries: %d  total associations: %d", col.totalEntries, col.totalAssociations)

	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	t.Logf("HeapAlloc after load: %s   Sys: %s", bytesH(m.HeapAlloc), bytesH(m.Sys))

	if p := os.Getenv("TIE_LOAD_HEAP"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create heap profile: %v", err)
		}
		defer f.Close()
		if err := pprof.WriteHeapProfile(f); err != nil {
			t.Fatalf("write heap profile: %v", err)
		}
		t.Logf("heap profile -> %s", p)
	}

	runtime.KeepAlive(col)
}

// splitTiePath splits /dir/name.tie into ("/dir", "name").
func splitTiePath(path string) (dir, base string) {
	slash := -1
	dot := -1
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			slash = i
		}
	}
	dir = "."
	base = path
	if slash >= 0 {
		dir = path[:slash]
		base = path[slash+1:]
	}
	for i := 0; i < len(base); i++ {
		if base[i] == '.' {
			dot = i
		}
	}
	if dot >= 0 {
		base = base[:dot]
	}
	return dir, base
}
