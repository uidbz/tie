package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"path/filepath"
	"strings"

	// "git.sr.ht/~uid/tie-client"
	"github.com/h2non/filetype"
)

func TypeSpecificTags(path, relPath string) error {
	fmt.Println("TypeSpecific:", path, "Relpath", relPath)
	newEntry := path
	// dir := "directory"
	is := "is"
	file := "file"
	contains := "contains"

	d := filepath.Dir(path)

	base := filepath.Base(path)

	parts := strings.Split(base, " ")
	for _, x := range parts {
		TieAssociate(x, newEntry, is)
	}

	dirsfiles := strings.Split(base, "/")
	for _, x := range dirsfiles {
		TieAssociate(x, newEntry, is)
	}

	TieAssociate(d, newEntry, contains)
	TieAssociate(base, newEntry, is)
	TieAssociate(newEntry, file, is)
	TieAssociate(file, newEntry, is)

	ext := filepath.Ext(path)

	switch ext {
	case "md":

	}

	f, err := os.Open(path)
	defer f.Close()

	if err != nil {
		return err
	}

	head := make([]byte, 261)
	n, err2 := f.Read(head)

	if err2 != nil {
		fmt.Println(path, ": returning nil :-(")
		return nil
	}

	if n != 261 {
		fmt.Println(path, ": read != 216. Actual", n)
		return nil
	}

	if filetype.IsArchive(head) {
		return nil
	}

	if filetype.IsAudio(head) {
		// fmt.Println(db.store.GetValueStringFromNode(file))
		// fmt.Println(db.store.GetValueStringFromNode(db))
		f.Seek(0, 0)
		IndexAudio(newEntry, f)
		return nil
	}

	if filetype.IsDocument(head) {

		return nil
	}

	if filetype.IsFont(head) {
		return nil
	}

	if filetype.IsImage(head) {
		TieAssociate(relPath, "type", "image")
		thumbnailDir := "/tmp/thumbs"
		os.MkdirAll(thumbnailDir+"/"+relPath[1:len(relPath)-3], 0755)
		CreateImageThumbnail(path, thumbnailDir+relPath[1:len(relPath)])
		return nil
	}
	if filetype.IsVideo(head) {
		TieAssociate(relPath, "type", "video")
		thumbnailDir := "/tmp/thumbs"
		os.MkdirAll(thumbnailDir+"/"+relPath[1:len(relPath)-3], 0755)
		CreateVideoThumbnail(path, thumbnailDir+relPath[1:len(relPath)])
		return nil
	}
	return nil
}

func CreateImageThumbnail(source, dest string) {
	args := []string{
		source,
		"-thumbnail", "200x200^",
		"-gravity", "center",
		"-extent", "200x200",
		"-auto-orient",
		dest,
	}
	cmd := exec.Command("convert", args...) //source+" -thumbnail 200x200^ -gravity center -extent 200x200 -auto-orient "+dest)
	if err := cmd.Run(); err != nil {
		fmt.Println(err)
	}
}

func CreateVideoThumbnail(source, dest string) {
	args := []string{
		// source,
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
		for i := 1.0; i <= 5; i++ {
			args2 := []string{
				"-ss", strconv.FormatFloat(sec*(0.1*i), 'f', 1, 64),
				"-i", source,
				"-y",
				"-vframes", "1",
				dest + "-" + strconv.Itoa(int(i)) + ".png",
			}
			cmd := exec.Command("ffmpeg", args2...) //source+" -thumbnail 200x200^ -gravity center -extent 200x200 -auto-orient "+dest)
			if err := cmd.Run(); err != nil {
				fmt.Println(err)
			} else {
				fmt.Println("Ok :-)")

			}
		}

	}
}
