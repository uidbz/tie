package client

import (
	"fmt"
	"strings"
	"testing"
)

// TestRestoreStreamAppliesEveryTriple: a dump larger than one chunk is applied
// whole, in several requests (§4.18).
func TestRestoreStreamAppliesEveryTriple(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}

	const n = 12000
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "key%05d\ttag\tshared\n", i)
	}

	var chunks []int
	total, err := tc.RestoreStream(strings.NewReader(sb.String()), func(done int) {
		chunks = append(chunks, done)
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != n {
		t.Errorf("RestoreStream read %d triples, want %d", total, n)
	}
	if len(chunks) < 3 {
		t.Errorf("the restore used %d batches for %d triples, want at least 3", len(chunks), n)
	}
	for _, prev := range chunks[:len(chunks)-1] {
		if prev%restoreBatchOps != 0 {
			t.Errorf("a chunk ended at %d triples, not a multiple of %d", prev, restoreBatchOps)
		}
	}
	if last := chunks[len(chunks)-1]; last != n {
		t.Errorf("the last chunk reported %d triples, want %d", last, n)
	}
	for _, key := range []string{"key00000", "key05999", "key11999"} {
		if !tc.Exists(key) {
			t.Errorf("%s is missing after the restore", key)
		}
	}
}

// TestRestoreStreamKeepsCompletedChunks is the reason for chunking beyond
// memory: when the input turns out to be malformed partway through, the chunks
// already sent are durable rather than lost with the one big request.
func TestRestoreStreamKeepsCompletedChunks(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&sb, "k%04d\ttag\tx\n", i)
	}
	sb.WriteString("this\tline\n") // two fields, not three
	for i := 6000; i < 7000; i++ {
		fmt.Fprintf(&sb, "k%04d\ttag\tx\n", i)
	}

	total, err := tc.RestoreStream(strings.NewReader(sb.String()), nil)
	if err == nil {
		t.Fatal("a malformed line was accepted")
	}
	if !strings.Contains(err.Error(), "6001") {
		t.Errorf("the error does not name the bad line: %v", err)
	}
	if total != 6000 {
		t.Errorf("it read %d triples before failing, want 6000", total)
	}

	if !tc.Exists("k0000") || !tc.Exists("k4999") {
		t.Error("the completed chunk was not applied")
	}
	if tc.Exists("k5000") {
		t.Error("triples past the malformed line were applied")
	}
}

// TestRestoreIsIdempotent: restoring the same dump twice merges, it does not
// double anything.
func TestRestoreIsIdempotent(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	if err := tc.DropCollection(); err != nil {
		t.Fatal(err)
	}

	triples := make([][3]string, 0, 6000)
	for i := 0; i < 6000; i++ {
		triples = append(triples, [3]string{fmt.Sprintf("k%04d", i), "tag", "twice"})
	}
	if err := tc.Restore(triples); err != nil {
		t.Fatal(err)
	}
	if err := tc.Restore(triples); err != nil {
		t.Fatal(err)
	}

	_, total, err := tc.Query(QuerySpec{
		Terms:   []string{"twice"},
		Filter:  "tag",
		Reverse: true,
		Limit:   -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6000 {
		t.Errorf("restoring twice left %d triples, want 6000", total)
	}
}
