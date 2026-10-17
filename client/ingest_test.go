package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uidbz/tie/metadata"
)

// tagTestData reuses the vendored tag reader's own fixtures: real tagged audio
// small enough to open a million times in a benchmark.
const tagTestData = "../metadata/tag/testdata/"

// TestIngestAgreesWithTheStandaloneHelpers: ingest is the same classification
// and the same tag read the standalone helpers produce, just from one open —
// and it must not lose the archive refinement or the tag parse.
func TestIngestAgreesWithTheStandaloneHelpers(t *testing.T) {
	for _, name := range []string{"dur.flac", "dur.mp3", "dur.ogg"} {
		path := filepath.Join(tagTestData, name)

		gotType, gotMedia, err := ingest(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantType, err := GetTieTypeFromPath(path)
		if err != nil {
			t.Fatalf("%s: classify: %v", name, err)
		}
		if gotType != wantType {
			t.Errorf("%s: ingest classified %v, GetTieTypeFromPath %v", name, gotType, wantType)
		}
		if wantMedia := ExtractMediaMetadata(path); gotMedia != wantMedia {
			t.Errorf("%s: ingest read %+v, ExtractMediaMetadata %+v", name, gotMedia, wantMedia)
		}
		if gotMedia.Duration == 0 {
			t.Errorf("%s: no tags read from a tagged test file (%+v)", name, gotMedia)
		}
	}
}

// TestIngestEmptyAndMissing: an empty file classifies as unknown without being
// an error (there are no bytes to sniff), and a missing path is one.
func TestIngestEmptyAndMissing(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tieType, media, err := ingest(empty)
	if err != nil {
		t.Errorf("an empty file is not an error: %v", err)
	}
	if tieType != TieUnknownFile || media != (metadata.Media{}) {
		t.Errorf("empty file: type %v media %+v, want unknown and no tags", tieType, media)
	}

	if _, _, err := ingest(filepath.Join(dir, "nope.bin")); err == nil {
		t.Error("a missing path is not reported as an error")
	}
}

// BenchmarkIngest is the per-file content read of an import: classify the type
// and parse the embedded tags. BenchmarkClassifyThenExtract is the same work in
// the shape imports used before §5.2 item 3 was fixed — two opens of the file
// and the same 261-byte sniff done twice.
func BenchmarkIngest(b *testing.B) {
	path := filepath.Join(tagTestData, "dur.flac")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ingest(path); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkClassifyThenExtract(b *testing.B) {
	path := filepath.Join(tagTestData, "dur.flac")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := GetTieTypeFromPath(path); err != nil {
			b.Fatal(err)
		}
		ExtractMediaMetadata(path)
	}
}
