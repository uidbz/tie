package main

import (
	"io"
	"os"
)

func Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return true, err
}

func FileCopy(src, dest string) (err error) {
	f, err := os.Create(dest)
	defer f.Close()
	if err != nil {
		return
	}

	if err = os.Chmod(f.Name(), 0755); err != nil {
		return
	}

	s, err := os.Open(src)
	defer s.Close()
	if err != nil {
		return
	}

	if _, err = io.Copy(f, s); err != nil {
		return
	}

	return
}
