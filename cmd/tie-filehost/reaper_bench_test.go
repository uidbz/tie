package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// benchStore builds a store with n plain blobs and d directory manifests.
func benchStore(b *testing.B, n, d int) (*server, *blobStore) {
	b.Helper()
	// The store logs its bootstrap scan; keep the bench output readable.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	root := b.TempDir()
	srv := &server{}
	if err := srv.initStores(FilehostConfig{BlobPaths: []BlobPathConfig{{Name: "s", Path: root}}}); err != nil {
		b.Fatal(err)
	}
	st := srv.storeByName("s")
	put := func(hash string, data []byte) {
		p, err := st.makeDestinationPath(hash)
		if err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0644); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		put(hexHash(fmt.Sprintf("%04x", i)), []byte("payload"))
	}
	for i := 0; i < d; i++ {
		h := hexHash(fmt.Sprintf("%04xe", i))
		put(h, []byte(metadata.DirHeader+"deadbeef\tkid\t7\t\t\t\t\n"))
		st.dirs.add(h)
	}
	return srv, st
}

// BenchmarkReaperMarkPhase measures the mark phase as it now runs: walk the
// recorded manifests and open only those.
func BenchmarkReaperMarkPhase(b *testing.B) {
	srv, st := benchStore(b, 2000, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, h := range st.dirs.all() {
			srv.markReferences(h, map[string]bool{}, map[string]bool{})
		}
	}
}

// BenchmarkReaperMarkPhaseLegacy measures what the mark phase used to cost:
// enumerate every blob and open each one looking for a manifest header.
func BenchmarkReaperMarkPhaseLegacy(b *testing.B) {
	srv, st := benchStore(b, 2000, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, h := range listBlobs(st.path) {
			p, err := PathFromHash(st.path, h)
			if err != nil {
				b.Fatal(err)
			}
			if isDirBlob(p) {
				srv.markReferences(h, map[string]bool{}, map[string]bool{})
			}
		}
	}
}
