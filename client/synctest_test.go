package client

import "testing"

// TestSyncInSurfacesServerFailure is the §5.1 L10 guard: SyncIn discarded the
// reply, so a server that refused the sync still looked like success to the
// caller — and callers use it to bound what a crash can lose.
func TestSyncInSurfacesServerFailure(t *testing.T) {
	tie := freshVerifyClient(t)

	// The server refuses a malformed collection name and answers Success:false;
	// SyncIn used to throw that reply away.
	if err := tie.In("bad/name").Sync(); err == nil {
		t.Error("SyncIn reported success for a request the server rejected")
	}

	// A live collection must still sync cleanly.
	if _, err := tie.Add("sync-key", "v1", "v2"); err != nil {
		t.Fatal(err)
	}
	if err := tie.Sync(); err != nil {
		t.Errorf("Sync on a live collection: %v", err)
	}
	if err := tie.Sync(); err != nil {
		t.Errorf("SyncIn on a live collection: %v", err)
	}
}
