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
	processDir   bool
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

	app.Process(fullpath)

	return nil
}

func (app *ExternalApp) Process(path string) {
	cmdArgs := append(app.args,
		"-input", path,
		"-settings", app.settingsFile,
		"-output", app.output)

	fmt.Println(app.name, cmdArgs)
	cmd := exec.Command(app.name, cmdArgs...)
	// cmd.CombinedOutput()             // TODO: Capture output
	out, err := cmd.CombinedOutput() // TODO: Capture output
	if err != nil {
		fmt.Println(string(out))
		fmt.Println("Error processing data:", err.Error())
	} else {
		fmt.Println(string(out))
	}
}

func (app *ExternalApp) PrintDebugInfo() {
	fmt.Println("Starting with settings:")
	fmt.Println("Url:", app.url)
	fmt.Println("Name:", app.name)
	fmt.Println("Args:", app.args)
	fmt.Println("Input hash:", app.inputHash)
	fmt.Println("Settings hash:", app.settingsFile)
	fmt.Println("Temp dir:", app.tmpDir)
	fmt.Println("Ouptut dir:", app.output)
	fmt.Println("Process dir:", app.processDir)
}

func main() {
	urlPtr := flag.String("url", "http://localhost:1162", "tie-serve url")
	appPtr := flag.String("app", "python", "application to execute")
	argsPtr := flag.String("args", "script.py", "args to app")
	//inputPtr := flag.String("input", "f72d4c34a7c7cc41bac64653ae4463d53891cb6d8577f5ebe3e483b6d5e02ac3", "input hash")
	inputPtr := flag.String("input", "c88a38e5d18a42980e2698ea1227fcb458204934203dfe912d3da2274cf2a7e6", "input hash")
	settingsPtr := flag.String("settings", "", "settings file hash")
	tmpDirPtr := flag.String("tempdir", "/tmp/tie-handle", "temp dir")
	processDirPtr := flag.Bool("processdir", true, "process dir, instead of individidual files")
	debugPtr := flag.Bool("debug", false, "Enable verbose output")

	flag.Parse()

	app := ExternalApp{
		url:        *urlPtr,
		name:       *appPtr,
		inputHash:  *inputPtr,
		args:       []string{*argsPtr},
		tmpDir:     filepath.Join(*tmpDirPtr, *inputPtr+*settingsPtr),
		processDir: *processDirPtr,''
	}
	app.output = app.tmpDir + "-output"

	if *debugPtr {
		app.PrintDebugInfo()
	}

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

	if *processDirPtr {
		err := getlib.DownloadFile(app.url, app.inputHash, app.tmpDir)
		if err != nil {
			fmt.Println("Error downloading input:", err.Error())
		}
		app.Process(app.tmpDir)
	} else {
		getlib.ExecForEach(app.url, app.inputHash, &app, "")
	}

	status := putlib.Upload(app.url, app.output, putlib.PutConfig{PathToWorkdir: true})
	if status.ErrorMsg != "" {
		fmt.Println("Error uploading output:", status.ErrorMsg)
	}

	fmt.Println("tie-handle-result:", status.LastItem.Hash)

	os.RemoveAll(app.tmpDir)
	os.RemoveAll(app.output)
}
