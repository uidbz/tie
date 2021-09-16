// tie-handle project main.go
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"git.sr.ht/~uid/tie/io/getlib"
	"git.sr.ht/~uid/tie/io/putlib"
)

type ExternalApp struct {
	url          string
	name         string
	inputHash    string
	args         []string
	tmpDir       string
	output       string
	settingsFile string
}

func (app *ExternalApp) Run(file io.Reader, relPath string) (err error) {
	fullpath := filepath.Join(app.tmpDir, relPath)
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
	cmdArgs := append(app.args,
		"-input", fullpath,
		"-settings", app.settingsFile,
		"-output", app.output)
	cmd := exec.Command(app.name, cmdArgs...)
	cmd.CombinedOutput() // TODO: Capture output

	return nil
}

func main() {
	urlPtr := flag.String("url", "http://localhost:1162", "tie-serve url")
	appPtr := flag.String("app", "python", "application to execute")
	argsPtr := flag.String("args", "script.py", "args to app")
	//inputPtr := flag.String("input", "f72d4c34a7c7cc41bac64653ae4463d53891cb6d8577f5ebe3e483b6d5e02ac3", "input hash")
	inputPtr := flag.String("input", "c88a38e5d18a42980e2698ea1227fcb458204934203dfe912d3da2274cf2a7e6", "input hash")
	settingsPtr := flag.String("settings", "", "settings file hash")
	tmpDirPtr := flag.String("tempdir", "/tmp/tie-handle", "temp dir")

	app := ExternalApp{
		url:       *urlPtr,
		name:      *appPtr,
		inputHash: *inputPtr,
		args:      []string{*argsPtr},
		tmpDir:    filepath.Join(*tmpDirPtr, *inputPtr+*settingsPtr),
	}

	app.output = app.tmpDir + "-output"
	if err := os.MkdirAll(app.output, 0755); err != nil {
		fmt.Println("Error making output directory: " + err.Error())
		return
	}

	if *settingsPtr != "" {
		app.settingsFile = filepath.Join(app.tmpDir, "tie-handle-settings.json")
		err := getlib.DownloadFile(app.url, app.inputHash, app.settingsFile)
		if err != nil {
			fmt.Println("Error downloading settings file:", err.Error())
			return
		}
	}

	getlib.ExecForEach(*urlPtr, *inputPtr, &app, "")
	status := putlib.Upload(app.url, app.output, putlib.PutConfig{PathToWorkdir: true})

	fmt.Println("tie-handle-result:", status.LastItem.Hash)
}
