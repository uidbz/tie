// Package daemon is the shared start-up skeleton for the tie servers
// (tie-triplestore, tie-filehost, tie-smb): the -config and -version flags, TOML
// config loading, logging setup, and signal waiting.
//
// Each server had its own copy of all of it and the copies drifted (§7.8) — the
// access-list checks in §5.1-L18 are the example that had to be fixed three
// times. Keeping the skeleton here means a fix to how a tie server starts lands
// once.
package daemon

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/uidbz/conf"

	"github.com/uidbz/tie/tielog"
	"github.com/uidbz/tie/version"
)

// Flags is what ParseFlags returns.
type Flags struct {
	// ConfigPath is the -config value.
	ConfigPath string
}

// ParseFlags registers and parses the two flags every server takes and installs
// its usage text. With -version it prints the build and exits. `usage` is the
// free-form block shown by -h.
func ParseFlags(name, defaultConfigPath, usage string) Flags {
	configPath := flag.String("config", defaultConfigPath, "Path to TOML config file.")
	showVersion := flag.Bool("version", false, "Print version and exit.")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintln(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println(name, version.String())
		os.Exit(0)
	}
	return Flags{ConfigPath: *configPath}
}

// LoadConfig reads a TOML config file into cfg, naming the file in the error.
func LoadConfig(path string, cfg any) error {
	if err := conf.ReadConfig(path, cfg); err != nil {
		return fmt.Errorf("reading config file %s: %w", path, err)
	}
	return nil
}

// SetupLogging installs the structured logger, records the running build, and
// returns the cleanup function each server defers. A log file that cannot be
// opened is reported on stderr and does not stop the server: losing the log
// file is bad, refusing to start is worse.
func SetupLogging(name, file, level string) (cleanup func()) {
	cleanup, err := tielog.Setup(tielog.Config{File: file, Level: level})
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not open log file, logging to stderr only:", err)
	}
	tielog.LogVersion(name)
	return cleanup
}

// WaitForSignal blocks until SIGINT or SIGTERM and returns the signal that
// arrived, so a server can shut down gracefully instead of being killed.
func WaitForSignal() os.Signal {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	return <-sig
}
