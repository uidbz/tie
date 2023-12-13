package client

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

//go:generate stringer -type=TieType -linecomment
//go:generate stringer -type=TieProperty -linecomment

type TieType int
type TieProperty int

const (
	TieImageFile       TieType = iota // image-file
	TieAudioFile                      // audio-file
	TieVideoFile                      // video-file
	TieDocumentFile                   // document-file
	TieImageDir                       // image-dir
	TieVideoDir                       // video-dir
	TieDocumentDir                    // document-dir
	TieImageArchive                   // image-archive
	TieVideoArchive                   // video-archive
	TieDocumentArchive                // document-archive
)

const (
	TieUid          TieProperty = iota // tie-uid
	TieDirectory                       // directory
	TieFilename                        // filename
	TieName                            // name
	TieMediaType                       // media-type
	TieFileHost                        // filehost
	TieTag                             // tag
	TieTags                            // tags
	TieTagDate                         // tag-date
	TieGalleryName                     // gallery-name
	TieTypeProperty                    // tie-type
	TieFiles                           // tie-files
	TieDirectories                     // tie-directories
	TieCategory                        // tie-category
	TieGalleries                       // tie-galleries
	TieAll                             // all
)

func str(t fmt.Stringer) string {
	return t.String()
}

func collection(tie *TieClient, tt TieType) string {
	var col string

	switch tt {
	case TieImageFile:
		col = tie.Config.Import.ImageCollection
	case TieVideoFile:
		col = tie.Config.Import.VideoCollection
	case TieDocumentFile:
		col = tie.Config.Import.DocumentCollection
	default:
		col = tie.Config.Import.GeneralCollection
	}

	return col
}

type TagInfo struct {
	Hash      string
	File      string
	MediaType string
	TieType   TieType
	Tags      []string
	Image     TagImage
}

type TagImage struct {
	GalleryName string
}

func EssentialTagInfo(hash, file, mimeType string, ft TieType, tags []string) TagInfo {
	return TagInfo{
		Hash:      hash,
		File:      file,
		MediaType: mimeType,
		TieType:   ft,
		Tags:      tags,
	}
}

func replaceVariables(str, file string) string {
	var dirname string
	if path, err := filepath.Abs(file); err == nil {
		dirname = filepath.Base(filepath.Dir(path))
	}
	str = strings.ReplaceAll(str, "$DIR", dirname)

	return str
}

func Tag(tie *TieClient, info TagInfo) error {
	origCollection := tie.Config.Collection
	defer func() {
		tie.Config.Collection = origCollection
	}()
	tie.Config.Collection = collection(tie, info.TieType)
	batch := tie.NewBatch()
	filename := filepath.Base(info.File)
	hash := info.Hash
	name := strings.TrimRight(filename, filepath.Ext(filename)) // Remove extension
	batch.Add(hash, str(TieFilename), filename)
	batch.Add(hash, str(TieName), name)
	batch.Add(hash, str(TieMediaType), info.MediaType)
	batch.Add(hash, str(TieTypeProperty), str(info.TieType))
	batch.Add(hash, str(TieTagDate), time.Now().Format(time.DateTime))
	for _, tag := range info.Tags {
		batch.Add(hash, str(TieTag), tag)
		batch.Add(str(TieTags), str(TieAll), tag)
	}

	switch info.TieType {
	case TieImageFile:
		if info.Image.GalleryName != "" {
			galleryName := replaceVariables(info.Image.GalleryName, info.File)
			batch.Add(hash, str(TieGalleryName), galleryName)
			batch.Add(str(TieGalleries), str(TieGalleryName), galleryName)
		}
		batch.Add(hash, str(TieCategory), str(TieFiles))
	}

	var err error
	tie.Batch(batch, func(r BatchReply) {
		for _, a := range r.AddReplys {
			if a.Success {
				err = errors.New("Error tagging: " + a.OrigKey + "First error message: " + a.Message)
				break
			}
		}
	})

	return err
}
