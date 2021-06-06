package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"strconv"
	"strings"
	"text/template"

	"git.sr.ht/~uid/tie/tiedb"

	_ "embed"

	"github.com/yuin/goldmark"
)

//go:embed index.html
var htmlDocumentTemplate string

//go:embed selection.html
var htmlSelectionTemplate string

func ReadMDFile(filePath string) (string, error) {
	hashRoot := "/data/"
	mdBuf, err := ioutil.ReadFile(hashRoot + filePath)
	if err != nil {
		return "", fmt.Errorf("Failed to read file: %w", err)
	}

	var htmlBuf bytes.Buffer
	if err := goldmark.Convert(mdBuf, &htmlBuf); err != nil {
		return "", fmt.Errorf("Failed to convert Markdown to HTML: %w", err)
	}

	return string(htmlBuf.Bytes()), nil
}

type DocumentRenderer struct {
	template *template.Template
	data     DataDocument
}

type SelectionRenderer struct {
	template *template.Template
	data     DataSelection
}

type DataDocument struct {
	Title     string
	Content   string
	LinksFrom string
	LinksTo   string
}

type DataSelection struct {
	Title   string
	Options string
}

func GetDocumentRenderer(filename string, associated *tiedb.StringSliceSet, selected bool) (*DocumentRenderer, error) {
	fmt.Println("Filename:", filename)
	html, err := ReadMDFile(filename)
	if err != nil {
		log.Println(err)
	}
	tmpl, err := template.New("index").Parse(htmlDocumentTemplate)
	if err != nil {
		return &DocumentRenderer{}, fmt.Errorf("Failed to create renderer: %w", err)
	}

	var mdBuf1 string //* Option 1\n* Option2\n* Option 3")
	var mdBuf2 string //* Option 1\n* Option2\n* Option 3")
	for i, rel := range associated.Relations {
		if rel == tiedb.ASSOCIATED {
			x := associated.Associations[i]
			url := strings.Replace(x, "/", "&slash;", -1)
			if selected {
				url = "../" + url
			}
			x = "[" + x + "](" + url + ")"
			mdBuf1 += "* " + x + "\n"
		} else {
			x := associated.Associations[i]
			url := strings.Replace(x, "/", "&slash;", -1)
			if selected {
				url = "../" + url
			}
			x = "[" + x + "](" + url + ")"
			mdBuf2 += "* " + x + "\n"
		}
	}
	var htmlBuf1 bytes.Buffer
	if err := goldmark.Convert([]byte(mdBuf1), &htmlBuf1); err != nil {
		return nil, fmt.Errorf("Failed to convert Markdown to HTML: %w", err)
	}
	var htmlBuf2 bytes.Buffer
	if err := goldmark.Convert([]byte(mdBuf2), &htmlBuf2); err != nil {
		return nil, fmt.Errorf("Failed to convert Markdown to HTML: %w", err)
	}

	return &DocumentRenderer{
		template: tmpl,
		data: DataDocument{
			Title:     "", //title,
			Content:   html,
			LinksFrom: string(htmlBuf1.Bytes()),
			LinksTo:   string(htmlBuf2.Bytes()),
		},
	}, nil
}

func GetSelectionRenderer(filename string, options []string) (*SelectionRenderer, error) {
	tmpl, err := template.New("index").Parse(htmlSelectionTemplate)
	if err != nil {
		return &SelectionRenderer{}, fmt.Errorf("Failed to create renderer: %w", err)
	}

	var mdBuf1 string
	for i, x := range options {
		url := strings.Replace(x, "/", "&slash;", -1)
		x = "[" + x + "](" + url + "/" + strconv.Itoa(i) + ")"
		mdBuf1 += "* " + x + "\n"
	}
	var htmlBuf1 bytes.Buffer
	if err := goldmark.Convert([]byte(mdBuf1), &htmlBuf1); err != nil {
		return nil, fmt.Errorf("Failed to convert Markdown to HTML: %w", err)
	}

	return &SelectionRenderer{
		template: tmpl,
		data: DataSelection{
			Title:   "", //title,
			Options: string(htmlBuf1.Bytes()),
		},
	}, nil
}
