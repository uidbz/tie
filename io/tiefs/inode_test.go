package tiesfs

import (
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/uidbz/tie/client"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(client.FileHost{URL: "http://127.0.0.1:1"}, 0, false)
	t.Cleanup(func() { s.Close() })
	return s
}

// TestInodeIDIsStableAcrossStores is the §4.17 guard: inode numbers used to be
// handed out from a per-process counter, so the same file got a different inode
// after a restart — and the map that remembered them grew by one entry per
// distinct key ever listed and was never pruned, which leaked for months in a
// long-lived tie-smb mount.
func TestInodeIDIsStableAcrossStores(t *testing.T) {
	a := testStore(t)
	b := testStore(t)

	// b sees an unrelated key first: with a counter, every number it hands out
	// afterwards is shifted.
	b.InodeID("\x00query\x00some other listing")

	keys := []string{
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MemberKey("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "track01.flac"),
		"\x00query\x00tag=rock",
		"\x00readme",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}
	for _, k := range keys {
		want := a.InodeID(k)
		if got := b.InodeID(k); got != want {
			t.Errorf("inode for %q differs between two mounts of the same tree: %d vs %d", k, got, want)
		}
		// Stable within one store too.
		if again := a.InodeID(k); again != want {
			t.Errorf("inode for %q changed within one mount: %d then %d", k, want, again)
		}
	}
}

// TestInodeIDNeverZero: 0 means "no inode" to FUSE and to SMB file ids.
func TestInodeIDNeverZero(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 200000; i++ {
		if got := s.InodeID(fmt.Sprintf("key-%d", i)); got == 0 {
			t.Fatalf("InodeID returned 0 for key-%d", i)
		}
	}
}

// TestInodeIDsAreDistinct checks the collision risk of deriving the number
// from the key: for a library-sized sample the numbers must not collide.
func TestInodeIDsAreDistinct(t *testing.T) {
	s := testStore(t)
	seen := make(map[uint64]string, 50000)
	for i := 0; i < 50000; i++ {
		key := fmt.Sprintf("%064x", i)
		ino := s.InodeID(key)
		if prev, dup := seen[ino]; dup {
			t.Fatalf("keys %q and %q share inode %d", prev, key, ino)
		}
		seen[ino] = key
	}
}

// TestInodeIDDoesNotGrow is the leak guard: 200k distinct keys must not leave
// 200k map entries behind.
func TestInodeIDDoesNotGrow(t *testing.T) {
	s := testStore(t)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	for i := 0; i < 200000; i++ {
		s.InodeID(fmt.Sprintf("%064x", i))
	}

	runtime.GC()
	runtime.ReadMemStats(&after)
	// The keys themselves are allocated by the loop; what must not grow is what
	// the Store keeps. A remembered entry per key costs several MB here.
	grown := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grown > 2<<20 {
		t.Errorf("the store retained %d bytes after 200k inode lookups; inode numbers must be derived, not remembered", grown)
	}
}

// BenchmarkInodeID compares the derived number against the remembered one it
// replaces (a map lookup under a read lock), since FUSE asks per lookup.
func BenchmarkInodeID(b *testing.B) {
	s := NewStore(client.FileHost{URL: "http://127.0.0.1:1"}, 0, false)
	defer s.Close()
	keys := make([]string, 1000)
	for i := range keys {
		keys[i] = MemberKey(fmt.Sprintf("%064x", i), "member")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.InodeID(keys[i%len(keys)])
	}
}

func BenchmarkInodeIDRemembered(b *testing.B) {
	keys := make([]string, 1000)
	m := make(map[string]uint64, len(keys))
	for i := range keys {
		keys[i] = MemberKey(fmt.Sprintf("%064x", i), "member")
		m[keys[i]] = uint64(i + 1)
	}
	var mu sync.RWMutex
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mu.RLock()
		_ = m[keys[i%len(keys)]]
		mu.RUnlock()
	}
}
