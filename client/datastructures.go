package client

import (
	ws "git.sr.ht/~uid/tie/webservice"
)

type TieClient struct {
	client *ws.Client
	Config Config
}

type TieOutput map[string]map[string]map[string]bool

// type TieOutput2 struct {
// 	// Tripplets []Tripplet
// 	Columns []string
// 	Data    []request.ReplyGet
// }

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

type Config struct {
	configDir     string
	configPath    string
	Username      string
	Password      string
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
