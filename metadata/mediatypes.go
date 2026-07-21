package metadata

// Media carries per-file media metadata extracted client-side at import time
// (currently audio tags via dhowden/tag). It feeds import-destination templates
// and per-file metadata triples; absent fields stay zero.
type Media struct {
	Hash      string
	MediaType string
	Title     string
	Artist    string
	Album     string
	Year      int
	Track     int
}
