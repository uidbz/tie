package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"git.sr.ht/~uid/tie/metadata"
	"github.com/dhowden/tag"
	"github.com/h2non/filetype"
)

const (
	magicNumber = 261
)

func GetMetadata(path string, hash string) (json.RawMessage, string, error) {
	f, err := os.Open(path)
	defer f.Close()

	if err != nil {
		return nil, "", err
	}

	head := make([]byte, magicNumber)
	n, err2 := f.Read(head)

	if err2 != nil {
		info := metadata.Info{}
		info.Hash = hash
		info.MediaType = ""
		b, _ := json.Marshal(info)
		return b, "application/octet-stream", err2
	}

	if n != magicNumber {
		info := metadata.Info{}
		info.Hash = hash
		info.MediaType = http.DetectContentType(head)
		s := strings.Split(info.MediaType, "/")
		b, _ := json.Marshal(info)
		return b, s[0], nil
	}

	t, _ := filetype.Get(head)
	// uid := uidHash + "/" + t.MIME.Type + "/" + t.MIME.Subtype + "/" + hash

	if filetype.IsArchive(head) {
		info := metadata.Info{}
		info.Hash = hash
		info.MediaType = t.MIME.Type + "/" + t.MIME.Subtype
		b, _ := json.Marshal(info)

		return b, "archive", nil
	}

	if filetype.IsAudio(head) {
		info := metadata.Audio{}
		info.Hash = hash
		info.MediaType = t.MIME.Type + "/" + t.MIME.Subtype

		f.Seek(0, 0)
		m, err := tag.ReadFrom(f)

		if err == nil {
			info.Year = m.Year()
			info.Title = m.Title()
			info.Album = m.Album()
			info.Artist = m.Artist()
			i, _ := m.Track()
			info.Track = i
		}

		b, _ := json.Marshal(info)
		return b, "audio", nil
	}

	if filetype.IsDocument(head) {
		return nil, "", nil
	}

	if filetype.IsFont(head) {
		return nil, "", nil
	}

	if filetype.IsImage(head) {
		v := metadata.Image{}
		v.Hash = hash
		v.MediaType = t.MIME.Type + "/" + t.MIME.Subtype
		b, _ := json.Marshal(v)
		return b, "image", nil
	}

	if filetype.IsVideo(head) {
		v := metadata.Video{}
		v.Hash = hash
		v.MediaType = t.MIME.Type + "/" + t.MIME.Subtype
		b, _ := json.Marshal(v)
		return b, "video", nil
	}

	info := metadata.Info{}
	info.Hash = hash
	info.MediaType = http.DetectContentType(head)
	s := strings.Split(info.MediaType, "/")
	b, _ := json.Marshal(info)
	return b, s[0], nil
}
