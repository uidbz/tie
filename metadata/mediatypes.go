package metadata

type Info struct {
	Hash      string
	MediaType string
}

type Audio struct {
	Hash      string
	MediaType string
	Title     string
	Artist    string
	Album     string
	Year      int
	Track     int
}

type Video struct {
	Hash      string
	MediaType string
}

type Image struct {
	Hash      string
	MediaType string
	Filename  string
}

type Markdown struct {
	MediaType string
	Filename  string
}
