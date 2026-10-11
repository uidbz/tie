package metadata

// Media carries per-file media metadata extracted client-side at import time
// (audio tags via the vendored tie/tag fork of dhowden/tag). It feeds
// import-destination templates and per-file metadata triples; absent fields
// stay zero.
type Media struct {
	Hash      string
	MediaType string
	Title     string
	Artist    string
	// AlbumArtist is the track's album-level artist (the ALBUMARTIST tag),
	// e.g. "Various Artists" for a compilation. Empty when untagged; consumers
	// fall back to Artist.
	AlbumArtist string
	Album       string
	Year        int
	Track       int
	// Disc is the disc number (the DISCNUMBER/TPOS tag), 0 when untagged.
	// DiscTotal is the total disc count when the tag carries it, else 0.
	// They feed disc-aware album placement (cd1/cd2 subdirectories).
	Disc, DiscTotal int
	// TrackTotal is the album's track count when the TRACKNUMBER tag carries
	// it ("3/12"), else 0.
	TrackTotal int
	// Genre is the raw GENRE tag; client.SplitGenres splits multi-value
	// spellings (";", "/", NUL) into individual genres. Kept as one string
	// so Media stays comparable.
	Genre    string
	Composer string
	Duration float64 // audio playing time in seconds, 0 when unknown
}
