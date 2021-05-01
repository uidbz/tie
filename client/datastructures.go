package tie

import (
	"git.sr.ht/~uid/tie/request"
)

type TieOutput map[string]map[string]map[string]bool

type TieOutput2 struct {
	// Tripplets []Tripplet
	Columns []string
	Data    []request.ReplyGet
}

// type Tripplet {
// 	Key string
// 	Value1 string
// 	Value2 string
// }

type AuthSuccess struct {
	/* variables */
}
type AuthError struct {
	/* variables */
}

type State struct {
	Namespace     string
	Collection    string
	Webservice    string
	ServeUrl      string
	DataHost      string
	ThumbnailHost string
	Key           []byte
	Verbose       bool
}

type TagOptions struct {
	AddOriginalPath               bool
	PutlibForceGenerateThumbnails bool
}
