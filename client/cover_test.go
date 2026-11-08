package client

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCoverNameScore(t *testing.T) {
	tests := []struct {
		name       string
		want       int
		confidence CoverConfidence
	}{
		{"cover.jpg", 100, CoverHigh},
		{"Cover.JPG", 100, CoverHigh},
		{"folder.jpg", 100, CoverHigh},
		{"Front.jpeg", 100, CoverHigh},
		{"frontcover.png", 100, CoverHigh},
		{"albumart.jpg", 100, CoverHigh},
		{"Artwork/front.jpg", 100, CoverHigh},
		{"artwork.jpg", 100, CoverHigh},
		{"00-artist-album-(front)-ys.jpg", 90, CoverHigh},
		{"front in.jpg", 90, CoverHigh},
		{"ins_front.jpg", 90, CoverHigh},
		{"cover1.jpg", 90, CoverHigh},
		{"front+back.jpg", 80, CoverMedium},
		{"back_front.jpg", 80, CoverMedium},
		{"AlbumArt_{FE88A25A}_Large.jpg", 80, CoverMedium},
		{"album-86711417aebcbfd14b876f3072cf1473-90171684ff82fe637b2ea6c021ba6e7b.jpeg", 70, CoverMedium},
		{"Artist - Album.jpg", 60, CoverMedium},
		{"01.jpg", 50, CoverMedium},
		{"albumartsmall.jpg", 40, CoverLow},
		{"AlbumArtSmall.jpg", 40, CoverLow},
		{"back.jpg", 10, CoverLow},
		{"00-va - the singles-2003-(back)-psycz.jpg", 10, CoverLow},
		{"cd.jpg", 10, CoverLow},
		{"scan_(0).JPG", 10, CoverLow},
		{"booklet front.jpg", 10, CoverLow},
		{"discotheque.jpg", 60, CoverMedium},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CoverNameScore(tt.name)
			if got != tt.want {
				t.Errorf("CoverNameScore(%q) = %d, want %d", tt.name, got, tt.want)
			}
			if c := coverConfidence(got); c != tt.confidence {
				t.Errorf("confidence(%q) = %v, want %v", tt.name, c, tt.confidence)
			}
		})
	}
}

// TestCoverPathPreference pins the "root beats a scans folder, .mediaartlocal
// is a fallback" rule.
func TestCoverPathPreference(t *testing.T) {
	if got, want := coverUserScore("cover.jpg", "cover.jpg"), coverUserScore("cover.jpg", "Artwork/cover.jpg"); got <= want {
		t.Errorf("root cover score %d <= artwork cover %d", got, want)
	}
	if got, want := coverUserScore("front.jpg", "Artwork/front.jpg"),
		coverUserScore("album-0123456789abcdef0123456789abcdef-0123456789abcdef0123456789abcdef.jpeg", ".mediaartlocal/album.jpeg"); got <= want {
		t.Errorf("artwork front score %d <= mediaart cache %d", got, want)
	}
	if coverConfidence(CoverNameScore("back.jpg")) != CoverLow {
		t.Error("back.jpg must be a low-confidence last resort")
	}
}

func TestResolveCoverSubdirBeatsBack(t *testing.T) {
	tc := NewTieClient(TestingConfig())
	requireServer(t, tc)
	root := uniqueTestPath(t)
	uid, err := tc.MkTieDirAll(root)
	if err != nil {
		t.Fatal(err)
	}
	art, err := tc.MkTieDirAll(root + "/Artwork")
	if err != nil {
		t.Fatal(err)
	}
	b := tc.NewBatch()
	for _, img := range []struct {
		hash, name string
		parent     DirUID
	}{
		{"back-image", "back.jpg", uid},
		{"front-image", "front.jpg", art},
	} {
		b.Add(img.hash, "tie-type", "image-file")
		b.Add(img.hash, "filename", img.name)
		b.Add(img.hash, "parent", string(img.parent))
	}
	if _, err := tc.Batch(b); err != nil {
		t.Fatal(err)
	}
	res, err := ResolveCover(tc, string(uid), CoverOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "Artwork/front.jpg" || res.Confidence != CoverHigh {
		t.Errorf("cover = %+v, want Artwork/front.jpg high", res)
	}
}

func TestRowCoverIsLow(t *testing.T) {
	row := Row{Key: "a", Attributes: map[string][]string{
		string(RelCover):       {"hash"},
		string(RelCoverSource): {"file:Artwork/back.jpg"},
	}}
	if !RowCoverIsLow(row) {
		t.Error("back.jpg cover must be low")
	}
	row.Attributes[string(RelCoverSource)] = []string{"file:Artwork/front.jpg"}
	if RowCoverIsLow(row) {
		t.Error("front.jpg cover must not be low")
	}
	row.Attributes[string(RelCoverSource)] = []string{"embedded:track"}
	if RowCoverIsLow(row) {
		t.Error("embedded cover must not be low")
	}
}

func TestZipCoverBytes(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range map[string]string{
		"scans/back.jpg":  "back",
		"scans/front.jpg": "front",
		"notes.txt":       "text",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	got, member, err := zipCoverBytes(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zipCoverBytes: %v", err)
	}
	if member != "scans/front.jpg" || string(got) != "front" {
		t.Errorf("zipCoverBytes = (%q, %q), want scans/front.jpg/front", got, member)
	}
}

func TestHTTPRangeReader(t *testing.T) {
	data := []byte("0123456789abcdef")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	r := &httpRangeReader{client: srv.Client(), url: srv.URL, size: int64(len(data))}
	got := make([]byte, 5)
	if n, err := r.ReadAt(got, 6); err != nil || n != 5 || string(got) != "6789a" {
		t.Errorf("ReadAt = (%d, %v) %q, want 6789a", n, err, got)
	}
	if n, err := r.ReadAt(make([]byte, 4), int64(len(data))); n != 0 || err == nil {
		t.Errorf("ReadAt EOF = (%d, %v), want 0/EOF", n, err)
	}
}

func TestTagBlockSize(t *testing.T) {
	head := make([]byte, 10)
	copy(head, "ID3")
	head[3], head[4] = 3, 0
	head[6], head[7], head[8], head[9] = 0x00, 0x00, 0x07, 0x68
	if n, ok := tagBlockSize(head); !ok || n != 1010 {
		t.Errorf("tagBlockSize(id3) = (%d, %v), want (1010, true)", n, ok)
	}
	if _, ok := tagBlockSize(make([]byte, 100)); ok {
		t.Error("tagBlockSize(garbage) ok, want false")
	}
}
