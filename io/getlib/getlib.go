package getlib

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func DownloadFile(url string, sourceHash string, basepath string, path string) (err error) {
	// Create the file

	// Get the data
	resp, err := http.Get(url + "/" + sourceHash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Check server response
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	var buf bytes.Buffer
	_, err = io.CopyN(&buf, resp.Body, 3)
	mode := string(buf.Bytes())
	_, err = io.Copy(&buf, resp.Body)

	if mode == "dir" {
		scanner := bufio.NewScanner(&buf)
		var begin bool
		for scanner.Scan() {
			input := scanner.Text()
			if input == "---" {
				begin = true
				continue
			}
			if begin {
				parts := strings.Split(input, "\t")
				if len(parts) == 2 {
					DownloadFile(url, parts[0], basepath, parts[1])
				}
			}
		}

		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
	} else {
		fullpath := filepath.Join(basepath, path)
		dir := filepath.Dir(fullpath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Println("Error making directories:", err.Error())
			log.Fatal("Exiting!")
		}
		out, err := os.Create(fullpath)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err2 := io.Copy(out, &buf)
		if err2 != nil {
			return err2
		}
	}

	return nil
}
