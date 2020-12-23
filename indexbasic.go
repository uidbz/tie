package main

type basic struct{}

func (_ *basic) defaultRelations() []string {
	relations := []string{
		"is",
		"contains",
		"value-is",
	}
	return relations
}
func (_ *basic) defaultHandlers() []string {
	handlers := []string{
		"open-with",
		"url",
		"directory",
		"parent",
		"child",
	}

	return handlers
}
func (_ *basic) defaultTypes() []string {
	types := []string{
		"file",
		"audio",
		"video",
		"image",
		"document",
		"archive",
		"font",
	}
	return types
}
