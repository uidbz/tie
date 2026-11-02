package main

// The project file: tie-project.toml describes one deployable image —
// what to build, how to run it, and the DeployKey that authorizes
// managing the resulting image on the runner.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/uidbz/tie/runner/api"

	toml "github.com/pelletier/go-toml/v2"
)

type TieProject struct {
	Name                     string
	Owner                    string
	Description              string
	DocumentationLink        string
	DeployKey                string // gates deploy access to an existing image
	Debug                    bool
	JobType                  string
	Source                   string // source tree dir, relative to the project
	TestApp                  string // injected app dir for test deploys
	TestInput                string
	TestOutput               string
	TestSettings             string
	Runner                   string // the runner to deploy to
	ServiceInternalPort      int
	ServiceAutostart         bool
	ServiceStartOnAccess     bool
	ServiceInactivityTimeout int
}

// loadTieProject reads and parses one project file.
func loadTieProject(path string) (TieProject, error) {
	var p TieProject
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := toml.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("parsing %s: %w", path, err)
	}
	return p, nil
}

// deployRequest maps the project onto the frozen wire DTO for POST
// /api/deploymanager: the client-only bookkeeping (Source, TestInput,
// TestSettings, Runner) stays in the project file; the runner gets the
// image metadata, service settings, and the hashes filled in by upload.
func (p TieProject) deployRequest() api.DeployRequest {
	return api.DeployRequest{
		Name:                     p.Name,
		Owner:                    p.Owner,
		Description:              p.Description,
		DocumentationLink:        p.DocumentationLink,
		DeployKey:                p.DeployKey,
		Debug:                    p.Debug,
		JobType:                  p.JobType,
		ServiceInternalPort:      p.ServiceInternalPort,
		ServiceAutostart:         p.ServiceAutostart,
		ServiceStartOnAccess:     p.ServiceStartOnAccess,
		ServiceInactivityTimeout: p.ServiceInactivityTimeout,
	}
}

// deployKeyLineRe matches the DeployKey assignment line in
// tie-project.toml.
var deployKeyLineRe = regexp.MustCompile(`^DeployKey[ \t]*=`)

// persistDeployKeyLine replaces the line matching ^DeployKey\s*= in the
// TOML file at path with `DeployKey = '<key>'`, or appends such a line if
// none exists. Every other byte of the file (comments, unknown keys,
// blank lines, CRLF endings) is preserved. The new content is written to
// a temp file in the same directory and renamed over the original, so the
// write is atomic: a failure leaves the original file untouched and
// readers never see a partial file.
func persistDeployKeyLine(path, key string) error {
	var old []byte
	if b, err := os.ReadFile(path); err == nil {
		old = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	mode := os.FileMode(0644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}

	// TOML literal string; fall back to a quoted basic string if the key
	// could not sit inside single quotes.
	value := "'" + key + "'"
	if strings.ContainsAny(key, "'\n\r") {
		value = strconv.Quote(key)
	}

	lines := bytes.Split(old, []byte("\n"))
	replaced := false
	for i, line := range lines {
		body, crlf := line, false
		if bytes.HasSuffix(body, []byte("\r")) {
			body, crlf = body[:len(body)-1], true
		}
		if deployKeyLineRe.Match(body) {
			newLine := "DeployKey = " + value
			if crlf {
				newLine += "\r"
			}
			lines[i] = []byte(newLine)
			replaced = true
			break
		}
	}

	out := old
	if replaced {
		out = bytes.Join(lines, []byte("\n"))
	} else {
		if len(out) > 0 && !bytes.HasSuffix(out, []byte("\n")) {
			out = append(out, '\n')
		}
		out = append(out, []byte("DeployKey = "+value+"\n")...)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeded
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
