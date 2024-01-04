package client

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"git.sr.ht/~uid/tie/io/putlib"
)

//go:generate stringer -type=TieType -linecomment
//go:generate stringer -type=TieProperty -linecomment

type TieType int
type TieProperty int
type TieCategory int

const (
	TieUnknownFile     TieType = iota // unknown-file
	TieImageFile                      // image-file
	TieAudioFile                      // audio-file
	TieVideoFile                      // video-file
	TieDocumentFile                   // document-file
	TieArchiveFile                    // archive-file
	TieImageDir                       // image-dir
	TieAudioDir                       // audio-dir
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
	TieCollection                      // collection
	TieTypeProperty                    // tie-type
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
	Hash          string
	File          string
	MediaType     string
	TieType       TieType
	Tags          []string
	Directory     string
	DirectoryType TieType
}

func EssentialTagInfo(hash, file, mediaType, directory string, ft TieType, dirType TieType, tags []string) TagInfo {
	directory = replaceVariables(directory, file)
	return TagInfo{
		Hash:          hash,
		File:          file,
		MediaType:     mediaType,
		Directory:     directory,
		DirectoryType: dirType,
		TieType:       ft,
		Tags:          tags,
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

func (tie *TieClient) ImportFile(file string, host string, tags []string, directory string, dirType TieType) error {
	fileType, err := GetTieTypeFromPath(file)
	if err != nil {
		return err
	}
	status := putlib.Upload(host, file, putlib.PutConfig{})
	for _, x := range status.UploadedItems {
		if x.ErrorMsg == "" {
			info := EssentialTagInfo(x.Hash, file, x.MediaType, directory, fileType, dirType, tags)
			Tag(tie, info)
		} else {
			return fmt.Errorf("Error uploading: %v\n%v\n", x.Filename, x.ErrorMsg)
		}
	}
	return nil
}

func (tie *TieClient) ImportDir(dir string, host string, dirType TieType, tags []string) error {
	status := putlib.Upload(host, dir, putlib.PutConfig{})
	if status.ErrorMsg == "" {
		info := EssentialTagInfo(status.LastItem.Hash, dir, status.LastItem.MediaType, filepath.Base(dir), dirType, dirType, tags)
		Tag(tie, info)
	} else {
		return fmt.Errorf("Error uploading: %v\n%v\n", status.LastItem.Filename, status.LastItem.ErrorMsg)
	}

	return nil
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
		if len(tag) > 0 {
			if tag[0] == '-' {
				batch.Delete(hash, str(TieTag), tag[1:])
			} else {
				batch.Add(hash, str(TieTag), tag)
				batch.Add(str(TieTags), str(TieAll), tag)
			}
		}
	}
	if info.Directory != "" {
		batch.Add(hash, str(TieDirectory), info.Directory)
		switch info.DirectoryType {
		case TieImageDir:
			batch.Add(info.Directory, str(TieTypeProperty), str(TieImageDir))
		case TieVideoDir:
			batch.Add(info.Directory, str(TieTypeProperty), str(TieVideoDir))
		case TieAudioDir:
			batch.Add(info.Directory, str(TieTypeProperty), str(TieAudioDir))
		}
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
