package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dhowden/tag"
)

type audio struct{}

func (_ *audio) defaultRelations() []string {
	relations := []string{}
	return relations
}
func (_ *audio) defaultHandlers() []string {
	handlers := []string{}
	return handlers
}
func (_ *audio) defaultTypes() []string {
	types := []string{
		"music",
		"year",
		"title",
		"artist",
		"album",
		"track",
	}
	return types
}

const (
	is       = "is"
	music    = "music"
	contains = "contains"
	year     = "year"
	title    = "title"
	artist   = "artist"
	album    = "album"
	track    = "track"
)

func IndexAudio(newEntry string, file *os.File) {
	TieAssociate(music, newEntry, contains)
	m, err := tag.ReadFrom(file)

	if err == nil {
		y := strconv.Itoa(m.Year())
		TieAssociate(y, newEntry, is)
		TieAssociate(newEntry, year, is)
		TieAssociate(y, newEntry, year)

		TieAssociate(m.Title(), newEntry, is)
		TieAssociate(newEntry, title, is)

		title_parts := strings.Split(m.Title(), " ")
		for _, x := range title_parts {
			TieAssociate(x, newEntry, is)
			TieAssociate(x, newEntry, title)
		}

		TieAssociate(m.Album(), newEntry, contains)
		TieAssociate(m.Album(), newEntry, album)

		TieAssociate(m.Artist(), newEntry, is)
		TieAssociate(newEntry, m.Artist(), is)

		track_no, _ := m.Track()
		track := strconv.Itoa(track_no)
		TieAssociate(track, newEntry, track)
		TieAssociate(newEntry, track, track)
	} else {
		fmt.Println("Error reading file.", "err:", err.Error())
	}
}
