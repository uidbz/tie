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
	"runtime"

	"git.sr.ht/~uid/tie/metadata"
)

// Used for cache path
const (
	shardLevels = 2 // directory nesting depth; must match tie-filehost
	dirWidth    = 2 // hex chars per level
)
const dirHeader = metadata.DirHeader

type TieFunc interface {
	Run(file io.Reader, relPath string) (err error)
}

// get issues an HTTP GET using the provided client, falling back to
// http.DefaultClient when nil. Pass a custom client to control TLS behavior
// (e.g. InsecureSkipVerify for self-signed filehosts).
func get(client *http.Client, url string) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return client.Get(url)
}

func DownloadFile(client *http.Client, url string, sourceHash string, destination string) (err error) {
	d := Download{destination: destination}

	return ExecForEach(client, url, sourceHash, &d, "")
}

type Cache struct {
	CacheDir    string
	SizeLimit   int64  // Max bytes
	HistoryFile string // Contain hash and file sizes
}

func InitCache(customLocation string) Cache {
	c := Cache{}
	c.SizeLimit = 0
	historyFile := ".getlib-cache-history"

	if customLocation != "" {
		c.CacheDir = customLocation
	} else {
		switch runtime.GOOS {
		case "linux":
			c.CacheDir, _ = os.UserCacheDir()

		case "windows":
			c.CacheDir, _ = os.UserCacheDir()

		case "android":
			c.CacheDir = os.Getenv("FILESDIR")
		}
		c.CacheDir = filepath.Join(c.CacheDir, "tie-cache")
	}

	c.HistoryFile = filepath.Join(c.CacheDir, historyFile)

	return c
}

func (c *Cache) ReadFile(client *http.Client, url string, sourceHash string) (file io.Reader, err error) {
	var dest string = c.CacheDir

	for i := 0; i < shardLevels*dirWidth; i += dirWidth {
		dest = filepath.Join(dest, sourceHash[i:i+dirWidth])
	}
	dir := dest
	dest = filepath.Join(dest, sourceHash)
	if _, exist := os.Stat(dest); os.IsNotExist(exist) {
		fmt.Println("getlib (not exist):", dest)
		os.MkdirAll(dir, 0755)
		out, err := os.Create(dest)
		if err != nil {
			return nil, err
		}
		f, err2 := ReadFile(client, url, sourceHash)
		if err2 != nil {
			os.Remove(dest)
			return nil, err2
		}
		r := io.TeeReader(f, out)
		return r, nil
	} else {
		return os.Open(dest)
	}
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

func IsDir(client *http.Client, url string, sourceHash string) (isDir bool, err error) {
	// Get the data
	resp, err := get(client, url+"/"+sourceHash)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	// Check server response
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("bad status: %s", resp.Status)
	}

	var buf bytes.Buffer
	_, err = io.CopyN(&buf, resp.Body, int64(len(dirHeader)))
	mode := string(buf.Bytes())

	if err != nil {
		return false, err
	}

	if mode == dirHeader {
		return true, nil
	} else {
		return false, nil
	}
}

func ReadFile(client *http.Client, url string, sourceHash string) (file io.Reader, err error) {
	// Get the data
	resp, err := get(client, url+"/"+sourceHash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check server response
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	var buf bytes.Buffer
	_, err = io.CopyN(&buf, resp.Body, int64(len(dirHeader)))
	mode := string(buf.Bytes())
	_, err = io.Copy(&buf, resp.Body)

	if mode == dirHeader {
		return nil, errors.New("Source is a directory; expected file.")
	} else {
		return &buf, nil
	}
}

func ReadBytes(client *http.Client, url string, sourceHash string) (b *bytes.Reader, err error) {
	// Get the data
	resp, err := get(client, url+"/"+sourceHash)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check server response
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}
	var buf bytes.Buffer
	_, err = io.CopyN(&buf, resp.Body, int64(len(dirHeader)))
	mode := string(buf.Bytes())
	_, err = io.Copy(&buf, resp.Body)

	if mode == dirHeader {
		return nil, errors.New("Source is a directory; expected file.")
	} else {
		return bytes.NewReader(buf.Bytes()), nil
	}
}

func ExecForEach(client *http.Client, url string, sourceHash string, funcToExec TieFunc, relPath string) (err error) {
	resp, err := get(client, url+"/"+sourceHash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	var buf bytes.Buffer
	io.CopyN(&buf, resp.Body, int64(len(dirHeader)))
	mode := string(buf.Bytes())
	_, err = io.Copy(&buf, resp.Body)
	if err != nil {
		return err
	}

	if mode == dirHeader {
		scanner := bufio.NewScanner(&buf)
		for scanner.Scan() {
			if entry, ok := metadata.ParseDirLine(scanner.Text()); ok {
				ExecForEach(client, url, entry.Hash, funcToExec, entry.Filename)
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
