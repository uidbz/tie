// Command tie-exec is the container entrypoint a runner invokes inside a
// job image. It is the thin process the runner's frozen container CLI
// contract expects: download (or FUSE-mount) the input by content hash
// from the filehost, optionally download an injected app tree, execute
// the real application with -input/-output/-settings, upload the output
// directory, and report the result hash through the runner's stdout
// marker protocol.
//
// Job images put this binary in their ENTRYPOINT with -app already set
// (the application the image provides); the runner appends -filehost,
// -inputhash, -store, -settings, -apphash and -debug. That argument
// vector is a wire contract, frozen together with the runner's run
// path - never rename a flag.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/io/fuselib"
	"github.com/uidbz/tie/io/getlib"
	"github.com/uidbz/tie/io/putlib"
	"github.com/uidbz/tie/runner/executor"
)

// Version information (set via -ldflags during build).
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

type externalApp struct {
	filehost        string
	store           string
	showVersion     bool
	name            string
	hash            string
	args            []string
	injectedAppHash string
	input           string
	output          string
	settings        string
	useFuse         bool
	cacheSizeGB     int
	debug           bool
}

func printDebugInfo(app externalApp) {
	fmt.Println("====== tie-exec settings: ======")
	fmt.Println("Filehost:", app.filehost)
	fmt.Println("Store:", app.store)
	fmt.Println("Name:", app.name)
	fmt.Println("Args:", app.args)
	fmt.Println("Input hash:", app.hash)
	fmt.Println("Settings file:", app.settings)
	fmt.Println("Input dir:", app.input)
	fmt.Println("Output dir:", app.output)
	fmt.Println("Injected app hash:", app.injectedAppHash)
	fmt.Println("================================")
}

func verifyCleanInput(app *externalApp) error {
	if app.name == "" {
		//lint:ignore ST1005 historic message, kept verbatim for operators
		return errors.New("Need -app flag to continue, see `tie-exec -help`")
	}

	if app.args[0] == "" {
		app.args = []string{}
	}

	return nil
}

// process runs the application. Its stdout and stderr are relayed to
// our stdout untouched: that stream is where the runner reads the
// marker protocol lines the application itself may emit. Wiring the
// streams directly (no pipe/scanner) avoids the pipe/Wait race and the
// 64KB line cap, and preserves the child's exit code.
func process(app externalApp) error {
	cmdArgs := []string{}
	if len(app.args) != 0 {
		cmdArgs = append(cmdArgs, app.args...)
	}
	cmdArgs = append(cmdArgs, "-input", app.input)
	cmdArgs = append(cmdArgs, "-output", app.output)
	if app.settings != "" {
		cmdArgs = append(cmdArgs, "-settings", app.settings)
	}
	if app.debug {
		fmt.Println(app.name, cmdArgs)
	}
	cmd := exec.Command(app.name, cmdArgs...)

	// Inherit environment variables to prevent libc segfaults in subprocess
	cmd.Env = os.Environ()

	// Provide stdin (even if closed) to prevent NULL dereference
	cmd.Stdin = os.Stdin

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout

	return cmd.Run()
}

func setError(errNo int, msg string) {
	log.Print(msg)
	os.Exit(errNo)
}

func parseFlags() externalApp {
	filehostPtr := flag.String("filehost", "https://localhost:1162", "tie-filehost url")
	storePtr := flag.String("store", "", "tie-filehost store to upload results to (Tie-Store header); empty uses the default store")

	appPtr := flag.String("app", "", "application to execute")
	argsPtr := flag.String("args", "", "args to app")

	hashPtr := flag.String("inputhash", "", "input hash")
	settingsPtr := flag.String("settings", "", "settings file")
	appHashPtr := flag.String("apphash", "", "injected app hash")

	inputDirPtr := flag.String("input", "/input", "input dir")
	outputDirPtr := flag.String("output", "/output", "output dir")

	ioPrefixPtr := flag.String("prefix", "/tmp", "Input + output prefix")

	usefusePtr := flag.Bool("usefuse", false, "Download files on demand, by mounting input via fuse")

	fuseCacheSize := flag.Int("cache-size-gb", 5, "Fuse RAM cache in GB (also the maximum file size)")

	debugPtr := flag.Bool("debug", false, "Enable verbose output")

	versionPtr := flag.Bool("version", false, "print version and exit")

	flag.Parse()

	return externalApp{
		filehost:        *filehostPtr,
		store:           *storePtr,
		showVersion:     *versionPtr,
		name:            *appPtr,
		hash:            *hashPtr,
		settings:        *settingsPtr,
		input:           filepath.Join(*ioPrefixPtr, *inputDirPtr),
		output:          filepath.Join(*ioPrefixPtr, *outputDirPtr),
		injectedAppHash: *appHashPtr,
		args:            []string{*argsPtr},
		useFuse:         *usefusePtr,
		cacheSizeGB:     *fuseCacheSize,
		debug:           *debugPtr,
	}
}

// protocol is the canonical marker protocol; Format/Parse share its
// definition, so the emitter here and the runner's parser cannot
// drift.
var protocol executor.Protocol

func main() {
	app := parseFlags()

	if app.showVersion {
		fmt.Printf("tie-exec version %s\nGit commit: %s\nBuilt: %s\n", Version, GitCommit, BuildDate)
		return
	}

	debugLog := func(msg string) {
		if app.debug {
			log.Println(msg)
		}
	}

	beginning := time.Now()
	defer func() { debugLog("Total execution time " + time.Since(beginning).String()) }()

	debugLog("Entered tie-exec")
	if e := verifyCleanInput(&app); e != nil {
		setError(1, e.Error())
	}

	if app.debug {
		printDebugInfo(app)
	}

	injectedAppPath := "/app"

	// Create output directory
	if err := os.MkdirAll(app.output, 0o755); err != nil {
		setError(1, "Error making output directory: "+err.Error())
	}

	if len(app.injectedAppHash) != 0 {
		debugLog("Downloading injected app...")
		start := time.Now()
		// Download hash to input directory
		os.MkdirAll(injectedAppPath, 0o755)
		if err := getlib.DownloadFile(nil, app.filehost, app.injectedAppHash, injectedAppPath, nil); err != nil {
			setError(1, "Error downloading injected app: "+err.Error())
		}
		debugLog("Finished download in " + time.Since(start).String())
	}
	if app.useFuse {
		debugLog("Mounting input... (download on demand)")
		os.MkdirAll(app.input, 0o755)
		state := fuselib.NewTieFuse(client.FileHost{URL: app.filehost, Store: app.store}, app.cacheSizeGB, false)
		server, err := state.Mount(app.hash, app.input)
		if err != nil {
			setError(1, "Error mounting input: "+err.Error())
		}
		defer server.Unmount()
		defer state.Close()
	} else {
		debugLog("Downloading input...")
		start := time.Now()
		// Download hash to input directory
		if err := getlib.DownloadFile(nil, app.filehost, app.hash, app.input, nil); err != nil {
			setError(1, "Error downloading input: "+err.Error())
		}
		debugLog("Finished download in " + time.Since(start).String())
	}

	debugLog("Start processing...")
	start := time.Now()
	// Process the input. A failing app must not produce a result: skip
	// upload and SetResult, report SetError, and exit with the child's
	// code (the runner turns that into a failed job).
	if err := process(app); err != nil {
		fmt.Println(protocol.Format(executor.ActionError, "tie-exec: application failed: "+err.Error()))
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
	debugLog("Finished processing in " + time.Since(start).String())

	debugLog("Uploading result...")
	start = time.Now()
	// Upload the result
	// Upload the result. LastItem carries the workdir's content address:
	// that becomes the job's output hash.
	status := putlib.Upload(app.filehost, app.output, putlib.PutConfig{Store: app.store})
	if status.ErrorMsg != "" {
		setError(1, "Error uploading output: "+status.ErrorMsg)
	}
	debugLog("Finished upload in " + time.Since(start).String())

	fmt.Println(protocol.Format(executor.ActionResult, status.LastItem.Hash))
}
