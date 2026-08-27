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
	Album     string
	Year      int
	Track     int
	Duration  float64 // audio playing time in seconds, 0 when unknown
}
