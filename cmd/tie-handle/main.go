// tie-handle project main.go
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"git.sr.ht/~uid/tie/io/getlib"
	"git.sr.ht/~uid/tie/io/putlib"
)

type ExternalApp struct {
	url        string
	name       string
	inputHash  string
	args       []string
	tmpDir     string
	output     string
	settings   string
	processDir bool
	debug      bool
	force      bool
	firstLog   bool
	statusDir  string
	statusFile string
	logFile    *os.File
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
	cmdArgs := []string{}
	if len(app.args) != 0 {
		cmdArgs = append(cmdArgs, app.args...)
	}
	if app.settings != "" {
		cmdArgs = append(cmdArgs, app.settings)
	}
	cmdArgs = append(cmdArgs, "-input", path)
	cmdArgs = append(cmdArgs, "-output", app.output)

	if app.debug {
		fmt.Println(app.name, cmdArgs)
	}
	cmd := exec.Command(app.name, cmdArgs...)
	outpPipe, _ := cmd.StdoutPipe()
	outputStream := bufio.NewScanner(outpPipe)
	errPipe, _ := cmd.StderrPipe()
	errStream := bufio.NewScanner(errPipe)
	cmd.Start()

	for outputStream.Scan() {
		app.LogOutput(outputStream.Text())
	}
	for errStream.Scan() {
		app.LogOutput(errStream.Text())
	}
	cmd.Wait()

	// out, err := cmd.CombinedOutput()
	// if err != nil {
	// 	fmt.Println(string(out))
	// 	fmt.Println("Error processing data:", err.Error())
	// } else {
	// 	fmt.Println(string(out))
	// }
}

func (app *ExternalApp) LogOutput(msg string) {
	fmt.Println(msg)

	if app.statusFile != "" {
		if app.firstLog {
			app.firstLog = false
		} else {
			msg = "\n" + msg
		}
		if _, err := app.logFile.WriteString(msg); err != nil {
			log.Fatal(err)
		}
	}
}

func (app *ExternalApp) PrintDebugInfo() {
	fmt.Println("Starting with settings:")
	fmt.Println("Url:", app.url)
	fmt.Println("Name:", app.name)
	fmt.Println("Args:", app.args)
	fmt.Println("Input hash:", app.inputHash)
	fmt.Println("Settings hash:", app.settings)
	fmt.Println("Temp dir:", app.tmpDir)
	fmt.Println("Ouptut dir:", app.output)
	fmt.Println("Process dir:", app.processDir)
}

func (app *ExternalApp) VerifyCleanInput() error {
	if app.name == "" {
		return errors.New("Need -app flag to continue, see `tie-handle -help`")
	}

	if app.args[0] == "" {
		app.args = []string{}
	}

	return nil
}

func main() {
	urlPtr := flag.String("url", "https://localhost:1162", "tie-serve url")
	appPtr := flag.String("app", "", "application to execute")
	argsPtr := flag.String("args", "", "args to app")
	inputPtr := flag.String("input", "", "input hash")
	settingsPtr := flag.String("settings", "", "settings file hash")
	tmpDirPtr := flag.String("tempdir", "/tmp/tie-handle", "temp dir")
	processDirPtr := flag.Bool("processdir", false, "process dir, instead of individidual files")
	debugPtr := flag.Bool("debug", false, "Enable verbose output")
	forcePtr := flag.Bool("force", false, "Ignore previous job status")
	statusDirPtr := flag.String("statusdir", "", "Write job status to file/check current status")

	flag.Parse()

	app := ExternalApp{
		url:        *urlPtr,
		name:       *appPtr,
		inputHash:  *inputPtr,
		args:       []string{*argsPtr},
		tmpDir:     filepath.Join(*tmpDirPtr, *inputPtr+*settingsPtr),
		processDir: *processDirPtr,
		debug:      *debugPtr,
		force:      *forcePtr,
		firstLog:   true,
		statusDir:  *statusDirPtr,
	}
	app.output = app.tmpDir + "-output"

	if e := app.VerifyCleanInput(); e != nil {
		fmt.Println(e.Error())
		return
	}

	if app.debug {
		app.PrintDebugInfo()
	}

	if app.statusDir != "" {
		app.statusDir = filepath.Join(app.statusDir, *inputPtr+*settingsPtr)
		app.statusFile = filepath.Join(app.statusDir, "tie-handle-status")
		if _, err := os.Stat(app.statusFile); err == nil {
			if app.force {
				os.Remove(app.statusFile)
			} else {
				if data, err := os.ReadFile(app.statusFile); err != nil {
					fmt.Println("Status files exists, but read-error happened:", err.Error())
					return
				} else {
					fmt.Println(string(data))
					return
				}
			}
		}

		if err := os.MkdirAll(app.statusDir, 0755); err != nil {
			fmt.Println("Error making status directory: " + err.Error())
			return
		}
		var err error
		app.logFile, err = os.OpenFile(app.statusFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
	}

	if err := os.MkdirAll(app.output, 0755); err != nil {
		app.LogOutput("Error making output directory: " + err.Error())
		return
	}

	if *settingsPtr != "" {
		app.settings = filepath.Join(app.tmpDir, "tie-handle-settings.json")
		err := getlib.DownloadFile(app.url, app.inputHash, app.settings)
		if err != nil {
			app.LogOutput("Error downloading settings file: " + err.Error())
			return
		}
	}

	if *processDirPtr {
		err := getlib.DownloadFile(app.url, app.inputHash, app.tmpDir)
		if err != nil {
			app.LogOutput("Error downloading input: " + err.Error())
		}
		app.Process(app.tmpDir)
	} else {
		getlib.ExecForEach(app.url, app.inputHash, &app, "")
	}
	status := putlib.Upload(app.url, app.output, putlib.PutConfig{PathToWorkdir: true})
	if status.ErrorMsg != "" {
		app.LogOutput("Error uploading output: " + status.ErrorMsg)
	}

	app.LogOutput("tie-handle-result: " + status.LastItem.Hash)

	app.logFile.Close()

	os.RemoveAll(app.tmpDir)
	os.RemoveAll(app.output)
}
