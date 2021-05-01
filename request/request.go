// tie-common project tie-common.go
package request

import (
	"git.sr.ht/~uid/tie/db"
)

type Request interface {
	Reply(*tiedb.Tree, tiedb.CollectionKey) string
}

type Add struct {
	Key    string
	Value1 string
	Value2 string
}

type Get struct {
	Values          []string
	NextLevelValues []string
	Filter          string
	MaxAssociations int
}

type Delete struct {
	Key    string
	Value1 string
	Value2 string
}

type Update struct {
	Entry1    string
	Entry2    string
	Relation  string
	NewEntry2 string
}

type ReplyGet struct {
	Item         string
	Associations []string
	Relations    []string
}

type ReplyStatus struct {
	Success bool
	Message string
}
