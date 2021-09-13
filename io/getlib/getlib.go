package getlib

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type TieFunc interface {
	Run(file io.Reader, relPath string) (err error)
}

func DownloadFile(url string, sourceHash string, destination string) (err error) {
	d := Download{destination: destination}

	return ExecForEach(url, sourceHash, &d, "")
}

type Download struct {
	destination string
}

func (d *Download) Run(file io.Reader, relPath string) (err error) {
	fullpath := filepath.Join(d.destination, relPath)
	dir := filepath.Dir(fullpath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return errors.New("Error making directories: " + err.Error())
	}
	dest, err := os.Create(fullpath)
	if err != nil {
		return err
	}
	defer dest.Close()
	_, err2 := io.Copy(dest, file)
	if err2 != nil {
		return err2
	}

	return nil
}

func IsDir(url string, sourceHash string) (isDir bool, err error) {
	// Get the data
	resp, err := http.Get(url + "/" + sourceHash)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	// Check server response
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("bad status: %s", resp.Status)
	}

	var buf bytes.Buffer
	_, err = io.CopyN(&buf, resp.Body, 3)
	mode := string(buf.Bytes())

	if err != nil {
		return false, err
	}

	if mode == "dir" {
		return true, nil
	} else {
		return false, nil
	}
}

func ReadFile(url string, sourceHash string) (file io.Reader, err error) {
	// Get the data
	resp, err := http.Get(url + "/" + sourceHash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check server response
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	var buf bytes.Buffer
	_, err = io.CopyN(&buf, resp.Body, 3)
	mode := string(buf.Bytes())
	_, err = io.Copy(&buf, resp.Body)

	if mode == "dir" {
		return nil, errors.New("Source is a directory; expected file.")
	} else {
		return &buf, nil
	}
}

func ExecForEach(url string, sourceHash string, funcToExec TieFunc, relPath string) (err error) {
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
					ExecForEach(url, parts[0], funcToExec, parts[1])
				}
			}
		}

		if err := scanner.Err(); err != nil {
			log.Fatal(err)
		}
	} else {
		return funcToExec.Run(&buf, relPath)
	}

	return nil
}
