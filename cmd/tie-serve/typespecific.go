package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func GenerateThumbnail(thumbnailDir string, path string, hash string, mediatype string) {
	if thumbnailDir == "" {
		return
	}
	for i := 0; i <= max; i = i + dirWidth {
		thumbnailDir = filepath.Join(thumbnailDir, hash[i:i+dirWidth])
	}
	switch mediatype {
	case "image":
		os.MkdirAll(thumbnailDir, 0755)
		CreateImageThumbnail(path, filepath.Join(thumbnailDir, hash))
	case "video":
		os.MkdirAll(thumbnailDir, 0755)
		CreateVideoThumbnail(path, filepath.Join(thumbnailDir, hash))
	}
}

// Create thumbnail using ImageMagick's convert program
func CreateImageThumbnail(source, dest string) {
	args := []string{
		source,
		"-thumbnail", "200x200^",
		"-gravity", "center",
		"-extent", "200x200",
		"-auto-orient",
		dest,
	}
	cmd := exec.Command("convert", args...)
	if err := cmd.Run(); err != nil {
		fmt.Println(err)
	}
}

// Create thumbnail using FFmpeg & ImageMagick's montage program
func CreateVideoThumbnail(source, dest string) {
	args := []string{
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		source,
	}
	cmd := exec.Command("ffprobe", args...)
	if out, err := cmd.Output(); err != nil {
		fmt.Println(err)
	} else {
		sec, err2 := strconv.ParseFloat(strings.Replace(string(out), "\n", "", 1), 64)
		if err2 != nil {
			fmt.Println("Error parsing duration:2", err2.Error())

		}
		pics := 40.0
		thumbs := make([]string, int(pics))
		for i := 0.99; i < pics; i++ {
			seconds := strconv.FormatFloat(sec*(1/pics*i), 'f', 1, 64)
			imgDest := dest + "-" + strconv.Itoa(int(i)) + ".jpg"
			argsFFmpeg := []string{
				"-ss", seconds,
				"-i", source,
				"-y",
				"-vframes", "1",
				imgDest,
			}
			thumbs[int(i)] = imgDest
			cmd := exec.Command("ffmpeg", argsFFmpeg...)
			if err := cmd.Run(); err != nil {
				fmt.Println("Error creating thumbnail:", err)
			}
		}
		argsMontage := append(thumbs, []string{
			"-geometry", "x720>+2+2",
			"-tile", "4x10",
			dest + "-screens.jpg",
		}...)
		cmd2 := exec.Command("montage", argsMontage...)
		if err := cmd2.Run(); err != nil {
			fmt.Println("Error generating screens:", err.Error())
		}
	}
}
