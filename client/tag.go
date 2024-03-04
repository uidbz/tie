package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~uid/tie/io/putlib"
	"github.com/go-resty/resty/v2"
)

//go:generate stringer -type=TieType -linecomment
//go:generate stringer -type=TieProperty -linecomment

type TieType int
type TieProperty int
type TieCategory int
type DirUID string

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
	TiePath                            // path
	TieParent                          // parent
	TieTags                            // tags
	TieTagDate                         // tag-date
	TieCollection                      // collection
	TieTypeProperty                    // tie-type
	TieAll                             // all
)

const (
	FileURIScheme string = "file:"
)

func (d DirUID) String() string {
	return string(d)
}

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
	Directory     DirUID
	DirectoryType TieType
}

// func EssentialTagInfo(hash, file, mediaType string, directory DirUID, ft TieType, dirType TieType, tags []string) TagInfo {
// 	directory = replaceVariables(directory, file)
// 	return TagInfo{
// 		Hash:          hash,
// 		File:          file,
// 		MediaType:     mediaType,
// 		Directory:     directory,
// 		DirectoryType: dirType,
// 		TieType:       ft,
// 		Tags:          tags,
// 	}
// }

func (tie *TieClient) ImportFile(file string, host string, tags []string, directory DirUID, dirType TieType) error {
	fileType, err := GetTieTypeFromPath(file)
	if err != nil {
		return err
	}
	status := putlib.Upload(host, file, putlib.PutConfig{})
	for _, x := range status.UploadedItems {
		if x.ErrorMsg == "" {
			info := TagInfo{
				Hash:          x.Hash,
				File:          file,
				MediaType:     x.MediaType,
				Directory:     directory,
				DirectoryType: dirType,
				TieType:       fileType,
				Tags:          tags,
			}
			// info := TagInfo {x.Hash, file, x.MediaType, directory, fileType, dirType, tags)
			Tag(tie, info)
		} else {
			return fmt.Errorf("Error uploading: %v\n%v\n", x.Filename, x.ErrorMsg)
		}
	}
	return nil
}

func (tie *TieClient) ImportDir(dir string, host string, parentDir DirUID, dirType TieType, tags []string) error {
	status := putlib.Upload(host, dir, putlib.PutConfig{})
	if status.ErrorMsg == "" {
		info := TagInfo{
			Hash:          status.LastItem.Hash,
			File:          dir,
			MediaType:     status.LastItem.MediaType,
			Directory:     parentDir,
			DirectoryType: dirType,
			TieType:       dirType,
			Tags:          tags,
		}
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
		batch.Add(hash, str(TieDirectory), str(info.Directory))
		switch info.DirectoryType {
		case TieImageDir:
			batch.Add(str(info.Directory), str(TieTypeProperty), str(TieImageDir))
		case TieVideoDir:
			batch.Add(str(info.Directory), str(TieTypeProperty), str(TieVideoDir))
		case TieAudioDir:
			batch.Add(str(info.Directory), str(TieTypeProperty), str(TieAudioDir))
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

func (tie *TieClient) DirUIDFromPath(path string) (DirUID, error) {
	o := GetOptions{
		Reverse: true,
		Filter:  str(TiePath),
	}
	if !strings.HasPrefix(path, FileURIScheme) {
		path = FileURIScheme + path
	}
	var err error
	var uid DirUID
	tie.Get(path, o, func(r GetReply) {
		if r.Success {
			if len(r.Result) > 1 {
				err = errors.New(strconv.Itoa(len(r.Result)) + "UIDs found for path. Expected 1.")
			} else {
				for key, _ := range r.Result {
					uid = DirUID(key)
				}
			}
		} else {
			err = errors.New("error:'" + r.Message + "'")
		}
	})

	return uid, err
}

func (tie *TieClient) CreateTieRootDir() error {
	rootpath := FileURIScheme + "/"
	uid, _ := tie.DirUIDFromPath(rootpath)
	if uid != "" {
		return errors.New("Root dir already exists, with UID: " + uid.String())
	}

	id, err := tie.newDirUID()
	if err != nil {
		return err
	}
	uid = DirUID(id)

	b := tie.NewBatch()
	b.Add(str(uid), str(TieParent), str(uid))
	b.Add(str(uid), str(TiePath), rootpath)
	tie.Batch(b, func(r BatchReply) {
		if !r.Success {
			err = errors.New(r.Message)
		}
	})

	return err
}

type directory struct {
}

func (tie *TieClient) ReadTieDir(path string) (DirUID, error) {
	return "", nil
}

func (tie *TieClient) MkTieDir(path string) (DirUID, error) {
	if !strings.HasPrefix(path, FileURIScheme) {
		path = FileURIScheme + path
	}
	uid, _ := tie.DirUIDFromPath(path)
	if uid != "" { // Dir already exists
		return uid, errors.New("Cannot create directory '" + path + "': Directory exists")
	}

	parentPath := filepath.Dir(path)
	if parentPath == FileURIScheme {
		parentPath = FileURIScheme + "/"
	}

	id, err := tie.newDirUID()
	if err != nil {
		return "", err
	}
	uid = DirUID(id)

	parentUID, err := tie.DirUIDFromPath(parentPath)
	if err != nil {
		return "", err
	}

	b := tie.NewBatch()
	b.Add(str(uid), str(TieParent), str(parentUID))
	b.Add(str(uid), str(TiePath), path)
	tie.Batch(b, func(r BatchReply) {
		if !r.Success {
			err = errors.New(r.Message)
		}
	})

	return uid, err
}

type Uid struct {
	Uid string
}

func (tie *TieClient) newDirUID() (uid DirUID, err error) {
	var result Uid
	url := tie.Config.UIDService + "/newjsonid"
	resp, err := resty.New().R().Get(url)
	if err != nil {
		return DirUID(""), err
	}
	err = json.Unmarshal(resp.Body(), &result)
	if err != nil {
		return DirUID(""), err
	}

	return DirUID(result.Uid), nil
}
