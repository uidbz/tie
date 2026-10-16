package tiedb

import (
	"encoding/hex"
	"testing"
)

// Golden bytes for the three record encoders, captured from the original
// append-based implementation. The on-disk format is stable, so these are the
// contract: any encoder rewrite (e.g. writing into one fixed buffer instead of
// six tiny slices) must reproduce them byte for byte.
const (
	goldenEntry = "0b0002000000000000002a000000000000000700000000000000" +
		"68656c6c6f20776f726c6400000000000000000000000000"
	goldenHash = "0d00ffffffffffffffff0700000000000000" +
		"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	goldenAssoc = "0c000100000000000000640000000000000002000000000000002c01000000000000" +
		"0000000000000000c800000000000000"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad golden constant: %v", err)
	}
	return b
}

func TestRecordEncodersGolden(t *testing.T) {
	uv := &UniqueValue{ParentId: 7}
	copy(uv.Value[:], []byte("hello world"))
	got := EntryToBytes(2, 42, uv)
	if hex.EncodeToString(got) != goldenEntry {
		t.Errorf("EntryToBytes = %x, want %s", got, goldenEntry)
	}
	if len(got) != ENTRY_SIZE {
		t.Errorf("EntryToBytes length %d, want %d", len(got), ENTRY_SIZE)
	}

	raw := [32]byte{}
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	got = HashToBytes(7, raw)
	if hex.EncodeToString(got) != goldenHash {
		t.Errorf("HashToBytes = %x, want %s", got, goldenHash)
	}

	a := Triple{Level: 1, Key: 100, Value1Level: 0, Value1: 200, Value2Level: 2, Value2: 300}
	got = a.toBytes()
	if hex.EncodeToString(got) != goldenAssoc {
		t.Errorf("toBytes = %x, want %s", got, goldenAssoc)
	}

	// Round-trip through the decoders, which is what the store actually does.
	level, entryID, uv2 := (&Collection{}).bufToEntry(toArray(EntryToBytes(2, 42, uv)))
	if level != 2 || entryID != 42 || uv2.ParentId != 7 || string(trimNUL(uv2.Value[:])) != "hello world" {
		t.Errorf("entry round-trip mismatch: level=%d id=%d uv=%+v", level, entryID, uv2)
	}
	id, raw2 := (&Collection{}).bufToHash(toArray(HashToBytes(7, raw)))
	if id != 7 || raw2 != raw {
		t.Errorf("hash round-trip mismatch: id=%d", id)
	}
	tri, err := (&Collection{}).bufToAssociation(toArray(a.toBytes()))
	if err != nil || tri != a {
		t.Errorf("association round-trip mismatch: %+v err=%v", tri, err)
	}
}

func toArray(b []byte) [ENTRY_SIZE]byte {
	var out [ENTRY_SIZE]byte
	copy(out[:], b)
	return out
}

func trimNUL(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && b[i] == 0; i-- {
		b = b[:i]
	}
	return b
}
