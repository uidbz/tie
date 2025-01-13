package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	TieDirectory                      // directory
	TieFile                           // file
)

const (
	TieUid          TieProperty = iota // tie-uid
	TieFilename                        // filename
	TieFilesize                        // filesize
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

func StringToTieType(t string) TieType {
	switch t {
	case "image-file":
		return TieImageFile
	case "audio-file":
		return TieAudioFile
	case "video-file":
		return TieVideoFile
	case "document-file":
		return TieDocumentFile
	case "archive-file":
		return TieArchiveFile
	case "image-dir":
		return TieImageDir
	case "audio-dir":
		return TieAudioDir
	case "video-dir":
		return TieVideoDir
	case "document-dir":
		return TieDocumentDir
	case "image-archive":
		return TieImageArchive
	case "video-archive":
		return TieVideoArchive
	case "document-archive":
		return TieDocumentArchive
	case "directory":
		return TieDirectory
	case "file":
		return TieFile
	}

	return TieUnknownFile
}

func SliceToTieType(types []string) (t []TieType) {
	for _, x := range types {
		t = append(t, StringToTieType(x))
	}
	return t
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
	Size      int
	MediaType string
	TieType   TieType
	Tags      []string
	Directory DirUID
	IsDir     bool
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

func (tie *TieClient) ImportFile(file string, host string, tags []string, directory DirUID) error {
	fmt.Println("Importing:", file)
	fileType, err := GetTieTypeFromPath(file)
	if err != nil {
		return err
	}
	stat, err := os.Stat(file)
	if err != nil {
		return err
	}
	status := putlib.Upload(host, file, putlib.PutConfig{})

	if status.ErrorMsg == "" {
		info := TagInfo{
			Hash:      status.LastItem.Hash,
			File:      file,
			Size:      int(stat.Size()),
			MediaType: status.LastItem.MediaType,
			Directory: directory,
			TieType:   fileType,
			Tags:      tags,
			IsDir:     stat.IsDir(),
		}
		// info := TagInfo {x.Hash, file, x.MediaType, directory, fileType, dirType, tags)
		Tag(tie, info)
	} else {
		return fmt.Errorf("Error uploading: %v\n%v\n", status.LastItem.Filename, status.LastItem.ErrorMsg)
	}
	return nil
}

// TODO: FIX this
func (tie *TieClient) ImportDir(dir string, host string, parentDir DirUID, dirType TieType, tags []string) error {
	status := putlib.Upload(host, dir, putlib.PutConfig{})
	for _, x := range status.UploadedItems {
		if x.ErrorMsg == "" {
			info := TagInfo{
				Hash:      x.Hash,
				File:      dir,
				MediaType: x.MediaType,
				Directory: parentDir,
				TieType:   dirType,
				Tags:      tags,
			}
			Tag(tie, info)
		} else {
			return fmt.Errorf("Error uploading: %v\n%v\n", x.Filename, x.ErrorMsg)
		}
	}
	return nil
}

func Tag(tie *TieClient, info TagInfo) error {
	fmt.Println("tagging", info.Hash)
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
	batch.Add(hash, str(TieFilesize), strconv.Itoa(info.Size))
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
	if info.IsDir {
		batch.Add(hash, str(TieTypeProperty), str(TieDirectory))
	} else {
		batch.Add(hash, str(TieTypeProperty), str(TieFile))
	}
	if info.Directory != "" {
		batch.Add(hash, str(TieParent), str(info.Directory))
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
				err = errors.New("Multiple (" + strconv.Itoa(len(r.Result)) + ") UIDs found for path. Expected 1.")
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
	b.Add(str(uid), str(TieTypeProperty), str(TieDirectory))
	tie.Batch(b, func(r BatchReply) {
		if !r.Success {
			err = errors.New(r.Message)
		}
	})

	return err
}

type Directory struct {
	Paths      []string
	Uid        DirUID
	SubDirs    []SubDirectory
	Files      []File
	ParentUIDs []DirUID
}

type SubDirectory struct {
	Paths    []string
	Uid      DirUID
	DirTypes []TieType
}

type File struct {
	Filename  string
	Uid       string
	TieType   TieType
	MediaType string
}

func ReadTieDir(tie *TieClient, uid DirUID) (Directory, error) {
	var err error
	setError := func(msg string) {
		if err == nil { // only record first error
			err = errors.New(msg)
		}
	}
	var dir Directory
	tie.SimpleGet(string(uid), func(r GetReply) {
		if r.Success {
			r.Result.ForEachKey(func(key string) {
				dir.Uid = uid
				entry := r.Result[key]
				dir.Paths = entry[str(TiePath)].ToSlice()
				parents := entry[str(TieParent)].ToSlice()
				dir.ParentUIDs = make([]DirUID, 0, len(parents))
				for _, x := range parents {
					dir.ParentUIDs = append(dir.ParentUIDs, DirUID(x))
				}
			})
		} else {
			setError("error:'" + r.Message + "'")
		}
	})
	if err != nil {
		return dir, err
	}
	o := GetOptions{
		Reverse:      true,
		GetNextLevel: true,
	}
	tie.Get(string(uid), o, func(r GetReply) {
		if r.Success {
			r.Result.ForEachKey(func(key string) {
				meta := r.NextLevelResult[key]
				types := meta[str(TieTypeProperty)]
				switch true {
				case types.Has(str(TieDirectory)):
					subDir := SubDirectory{
						Uid:      DirUID(key),
						Paths:    meta[str(TiePath)].ToSlice(),
						DirTypes: SliceToTieType(types.ToSlice()),
					}
					dir.SubDirs = append(dir.SubDirs, subDir)
				case types.Has(str(TieImageFile)):
					fallthrough
				case types.Has(str(TieVideoFile)):
					fallthrough
				case types.Has(str(TieAudioFile)):
					fallthrough
				case types.Has(str(TieDocumentFile)):
					f := File{
						Uid:       key,
						Filename:  meta[str(TieFilename)].ToString(),
						TieType:   StringToTieType(types.ToString()),
						MediaType: meta[str(TieMediaType)].ToString(),
					}
					dir.Files = append(dir.Files, f)

				}
			})
		} else {
			setError("error:'" + r.Message + "'")
		}
	})

	return dir, err
}

// func (tie *TieClient) ReadTieDir(uid DirUID) (Directory, error) {
// 	o := GetOptions{
// 		GetNextLevel:    true,
// 		NextLevelFilter: str(TieTypeProperty),
// 	}
// 	var err error
// 	tie.Get(uid, o, func(r GetReply) {
// 		if r.Success {
// 			r.Result.ForEachValue2(key, val1, val2 string) {
// 				// Write here
// 			}
// 		} else {
// 			err = errors.New("error:'" + r.Message + "'")
// 		}
// 	})
// 	return "", nil
// }

func (tie *TieClient) MkTieDirAll(path string) (DirUID, error) {
	if strings.HasPrefix(path, FileURIScheme) {
		path, _ = strings.CutPrefix(path, FileURIScheme)
	}
	err := tie.CreateTieRootDir()
	if err != nil && !strings.HasPrefix(err.Error(), "Root dir already exists") {
		return "", err
	}
	parts := strings.Split(path, string(os.PathSeparator))
	if len(parts) < 1 {
		return "", errors.New("Incomplete path")
	}
	parts[0] = FileURIScheme
	var uid DirUID
	for i, _ := range parts {
		if i == 0 {
			continue
		}
		uid, err = tie.MkTieDir(filepath.Join(parts[0 : i+1]...))
		if err != nil && !strings.HasSuffix(err.Error(), "Directory exists") {
			fmt.Println("huh")
			fmt.Println(err.Error(), !strings.HasSuffix(err.Error(), "Directory exists"))
			return "", err
		} else {
			err = nil
		}
	}

	return uid, err
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
	b.Add(str(uid), str(TieTypeProperty), str(TieDirectory))
	tie.Batch(b, func(r BatchReply) {
		if !r.Success {
			err = errors.New(r.Message)
		}
	})

	return uid, err
}

func (tie *TieClient) SetDirType(uid DirUID, dirType TieType) (err error) {
	tie.Add(str(uid), str(TieTypeProperty), str(dirType), func(r AddReply) {
		if !r.Success {
			err = errors.New(r.Message)
		}
	})
	return err
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
