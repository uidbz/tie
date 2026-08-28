package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/uidbz/tie/metadata"
)

// retentionFileName is the reserved index file in the datastore root. The
// leading dot keeps it distinct from the 2-hex shard directories, and the
// 64-char hash validation on the download/upload paths already rejects it as a
// blob name, so it can never collide with stored content.
const retentionFileName = ".tie-retention.json"

// blobRetention is one entry in the retention index. A blob absent from the
// index is permanent (infinite retention); this keeps infinite the zero-write
// default and leaves every pre-existing blob permanent after an upgrade.
type blobRetention struct {
	// OwnerTokenHash is the hash of the owner token, never the token itself.
	// Empty means the blob has no owner and anyone may change its retention.
	OwnerTokenHash string `json:"owner_token_hash,omitempty"`
	// ExpiresAt is a unix timestamp; 0 means infinite (permanent).
	ExpiresAt int64 `json:"expires_at"`
	// UpdatedAt is the unix time the entry was last written.
	UpdatedAt int64 `json:"updated_at"`
}

// retentionIndex is the in-memory, mutex-guarded map of blob hash -> retention,
// persisted atomically to retentionFileName in the datastore root.
type retentionIndex struct {
	mu      sync.Mutex
	path    string
	entries map[string]blobRetention
}

// hashToken returns the stored form of an owner token: its tie content address.
// Reusing metadata.HashReader avoids adding a crypto dependency and keeps the
// token itself off disk.
func hashToken(token string) string {
	if token == "" {
		return ""
	}
	h, err := metadata.HashReader(strings.NewReader(token))
	if err != nil {
		return ""
	}
	return h
}

// loadRetentionIndex reads the index from the datastore root. A missing file is
// not an error: it yields an empty index (all blobs permanent).
func loadRetentionIndex(root string) (*retentionIndex, error) {
	ri := &retentionIndex{
		path:    filepath.Join(root, retentionFileName),
		entries: map[string]blobRetention{},
	}
	data, err := os.ReadFile(ri.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ri, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return ri, nil
	}
	if err := json.Unmarshal(data, &ri.entries); err != nil {
		return nil, err
	}
	if ri.entries == nil {
		ri.entries = map[string]blobRetention{}
	}
	return ri, nil
}

// save writes the index atomically (temp file + rename). Callers must hold mu.
func (ri *retentionIndex) save() error {
	data, err := json.MarshalIndent(ri.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := ri.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, ri.path)
}

// get returns the entry for a hash and whether it exists.
func (ri *retentionIndex) get(hash string) (blobRetention, bool) {
	ri.mu.Lock()
	defer ri.mu.Unlock()
	e, ok := ri.entries[hash]
	return e, ok
}

// authorized reports whether a caller presenting token may change the entry for
// hash. An unowned or absent entry is open to anyone; an owned entry requires
// the token whose hash matches.
func (ri *retentionIndex) authorized(hash, token string) bool {
	ri.mu.Lock()
	defer ri.mu.Unlock()
	e, ok := ri.entries[hash]
	if !ok || e.OwnerTokenHash == "" {
		return true
	}
	return e.OwnerTokenHash == hashToken(token)
}

// recordUpload sets retention at upload time using the extend-not-shrink rule:
// a later upload of identical bytes can lengthen an expiry (or make it
// permanent) but never shorten it, so a temporary re-upload can't cut short a
// permanent blob. An unowned entry adopts the presented owner token; an owned
// entry keeps its owner. now is the current unix time.
//
// blobExisted distinguishes the two upload paths. When the blob was already on
// disk, an absent index entry means the blob is already permanent (an earlier
// permanent upload records nothing, and pre-upgrade blobs have no entry), so a
// dedup re-upload can only extend — never introduce an expiry. Only a genuine
// first store (blobExisted == false) may create a fresh temporary entry.
func (ri *retentionIndex) recordUpload(hash string, expiresAt int64, token string, now int64, blobExisted bool) error {
	ri.mu.Lock()
	defer ri.mu.Unlock()
	e, ok := ri.entries[hash]
	switch {
	case ok:
		e.ExpiresAt = maxExpiry(e.ExpiresAt, expiresAt)
		if e.OwnerTokenHash == "" && token != "" {
			e.OwnerTokenHash = hashToken(token)
		}
	case blobExisted:
		// Already-stored blob with no entry is permanent; extend-only keeps it
		// permanent regardless of expiresAt. The only reason to write is to
		// claim ownership.
		if token == "" {
			return nil
		}
		e = blobRetention{OwnerTokenHash: hashToken(token)}
	default:
		// Genuine first store.
		if expiresAt == 0 && token == "" {
			return nil // permanent default — leave absent, no write
		}
		e = blobRetention{OwnerTokenHash: hashToken(token), ExpiresAt: expiresAt}
	}
	e.UpdatedAt = now
	ri.entries[hash] = e
	return ri.save()
}

// setRetention is the explicit owner action: it may both shorten and extend,
// and claims ownership if the entry is currently unowned. Callers must have
// already passed authorized(). now is the current unix time.
func (ri *retentionIndex) setRetention(hash string, expiresAt int64, token string, now int64) error {
	ri.mu.Lock()
	defer ri.mu.Unlock()
	e := ri.entries[hash]
	e.ExpiresAt = expiresAt
	if e.OwnerTokenHash == "" && token != "" {
		e.OwnerTokenHash = hashToken(token)
	}
	e.UpdatedAt = now
	ri.entries[hash] = e
	return ri.save()
}

// maxExpiry returns the longer-lived of two expiries, treating 0 (infinite) as
// the maximum.
func maxExpiry(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > b {
		return a
	}
	return b
}
