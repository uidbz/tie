package main

type AuthSuccess struct {
	/* variables */
}
type AuthError struct {
	/* variables */
}

type Success struct {
	Success bool
}

type Association struct {
	Entry1   string
	Relation string
	Entry2   string
}

type State struct {
	Namespace  string
	Collection string
	Webservice string
}

type RequestGet struct {
	Value           string
	MaxAssociations int
}

type ReplyGet struct {
	Item         string
	Associations []string
	Relations    []string
}
