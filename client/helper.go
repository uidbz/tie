package client

import (
	"io"
	"os"

	"github.com/h2non/filetype"
)

func GetTieType(file io.Reader) (TieType, error) {
	head := make([]byte, 261)
	if _, err := file.Read(head); err != nil {
		return TieUnknownFile, err
	}

	switch true {
	case filetype.IsImage(head):
		return TieImageFile, nil
	case filetype.IsVideo(head):
		return TieVideoFile, nil
	case filetype.IsAudio(head):
		return TieAudioFile, nil
	case filetype.IsDocument(head):
		return TieDocumentFile, nil
	case filetype.IsArchive(head):
		return TieArchiveFile, nil
	default:
		return TieUnknownFile, nil
	}
}

func GetTieTypeFromPath(path string) (TieType, error) {
	file, err := os.Open(path)
	defer file.Close()
	if err != nil {
		return TieUnknownFile, err
	}
	return GetTieType(file)
}
