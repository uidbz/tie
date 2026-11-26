package main

import (
	"testing"

	"github.com/uidbz/tie/client"
)

// TestMediaCoversAllClearsThumbnail: re-resolving a cover replaces the source
// image, so the cached thumbnail scaled from the old one must go too (SetCover's
// contract); an unchanged cover keeps its thumbnail.
func TestMediaCoversAllClearsThumbnail(t *testing.T) {
	a, _ := localApp(t)
	uid, err := a.tie.MkTieDirAll("tie:/music/Album")
	if err != nil {
		t.Fatal(err)
	}
	b := a.tie.NewBatch()
	b.Add("front-image", "tie-type", "image-file")
	b.Add("front-image", "filename", "front.jpg")
	b.Add("front-image", "parent", string(uid))
	b.Set(string(uid), string(client.RelCover), []string{"old-image"})
	b.Set(string(uid), string(client.RelThumbnail), []string{"old-thumb"})
	if _, err := a.tie.Batch(b); err != nil {
		t.Fatal(err)
	}

	run := func() client.Row {
		t.Helper()
		if _, stderr, code, err := runTie(t, a, "", "media", "covers", "--all",
			"--embedded=false", "--archives=false", "--parent=false", string(uid)); code != 0 {
			t.Fatalf("media covers exited %d: %v (%s)", code, err, stderr)
		}
		row, err := a.tie.Get(string(uid))
		if err != nil {
			t.Fatal(err)
		}
		return row
	}

	row := run()
	if got := client.RowFirst(row, client.RelCover); got != "front-image" {
		t.Fatalf("cover = %q, want front-image", got)
	}
	if got := client.RowValues(row, client.RelThumbnail); len(got) != 0 {
		t.Errorf("thumbnail = %v after the cover changed, want it cleared", got)
	}

	if err := a.tie.Set(string(uid), string(client.RelThumbnail), []string{"new-thumb"}); err != nil {
		t.Fatal(err)
	}
	row = run()
	if got := client.RowFirst(row, client.RelThumbnail); got != "new-thumb" {
		t.Errorf("thumbnail = %q after re-resolving the same cover, want it kept", got)
	}
}
