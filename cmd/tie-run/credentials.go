package main

// One credentials lookup for every command that asks "what runner and
// DeployKey manage image X?". Without it each command answers with
// different precedence and different messages. They all go through
// lookupTarget, so the precedence, the prompts, and the error texts are
// identical everywhere.

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// Target is a resolved (runner, DeployKey) pair plus where it came from,
// so callers and error messages can name the source.
type Target struct {
	Runner    string
	DeployKey string
	Source    string // "tie-project.toml", "tie-run-config.toml", or "prompt"
}

// lookupTarget resolves image -> (runner, DeployKey): the
// tie-project.toml in the current directory when its Name matches the
// image, then tie-run-config.toml (written by deploys), then — only when
// interactive and stdin is a real terminal — a prompt with hidden key
// input. The not-found error names both config sources so the fix is
// obvious.
func lookupTarget(image string, interactive bool) (Target, error) {
	if runner, key, err := projectRunnerAndKey(image); err == nil {
		return Target{Runner: runner, DeployKey: key, Source: projectFile}, nil
	}
	// A config that exists but fails to load is a real error, not a miss:
	// propagate it instead of falling through to the prompt.
	cfg, path, err := loadTieRunConfig()
	if err != nil {
		return Target{}, err
	}
	if entry, ok := cfg.Images[image]; ok && entry.DeployKey != "" {
		if entry.Runner == "" {
			return Target{}, fmt.Errorf("no runner for image %q in %s — re-run the deploy in that project first", image, path)
		}
		return Target{Runner: entry.Runner, DeployKey: entry.DeployKey, Source: "tie-run-config.toml"}, nil
	}
	if !interactive {
		return Target{}, fmt.Errorf("no DeployKey for %q: not in %s (current directory) or tie-run-config.toml — deploy it first, or run this in the project directory", image, projectFile)
	}
	// Prompt, but only on a real terminal — never block a pipe/CI run.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return Target{}, fmt.Errorf("DeployKey not found for %q and stdin is not a terminal: run this in the project directory, or run interactively", image)
	}
	fmt.Printf("DeployKey not found for '%s' in %s or tie-run-config.toml\n", image, projectFile)
	fmt.Print("Enter runner URL: ")
	runner, err := readLine()
	if err != nil {
		return Target{}, err
	}
	if strings.TrimSpace(runner) == "" {
		return Target{}, fmt.Errorf("runner URL is required")
	}
	// Hidden input: fmt.Scanln echoed the key to the terminal and stopped
	// at the first space.
	fmt.Print("Enter DeployKey: ")
	key, err := readHiddenLine()
	if err != nil {
		return Target{}, err
	}
	if strings.TrimSpace(key) == "" {
		return Target{}, fmt.Errorf("DeployKey is required")
	}
	return Target{Runner: strings.TrimSpace(runner), DeployKey: key, Source: "prompt"}, nil
}

// projectRunnerAndKey loads runner and DeployKey from tie-project.toml in
// the current directory. Returns error if the file doesn't exist, the
// Name doesn't match, or either field is unset — callers then fall
// through to tie-run-config.toml.
func projectRunnerAndKey(imageName string) (runner, deployKey string, err error) {
	var project TieProject
	if project, err = loadTieProject(projectFile); err != nil {
		return "", "", fmt.Errorf("%s not found in current directory", projectFile)
	}
	if project.Name != imageName {
		return "", "", fmt.Errorf("image name mismatch: %s has '%s', requested '%s'", projectFile, project.Name, imageName)
	}
	if project.DeployKey == "" {
		return "", "", fmt.Errorf("DeployKey not set in %s", projectFile)
	}
	if project.Runner == "" {
		return "", "", fmt.Errorf("the Runner key is not set in %s", projectFile)
	}
	return project.Runner, project.DeployKey, nil
}

// deployKeyFor looks up just the DeployKey for a handler image (the
// runner comes from the job/pipeline itself on those paths). It shares
// lookupTarget's precedence and messages.
func deployKeyFor(image string) (string, error) {
	t, err := lookupTarget(image, false)
	if err != nil {
		return "", err
	}
	return t.DeployKey, nil
}

// termIsTerminal reports whether interactive prompts are safe.
func termIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// readLine reads one line from stdin (used for the interactive prompts
// that may legitimately echo).
func readLine() (string, error) {
	var line string
	if _, err := fmt.Scanln(&line); err != nil {
		// Scanln errors on EOF/empty input; treat as empty so callers can
		// report the missing value rather than a bare scan error.
		return "", nil
	}
	return line, nil
}

// readHiddenLine reads a secret (DeployKey) from stdin without echoing
// it, and without stopping at spaces the way fmt.Scanln does. Callers
// must have already checked term.IsTerminal.
func readHiddenLine() (string, error) {
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println() // ReadPassword swallows the Enter keystroke; redraw the newline
	if err != nil {
		return "", fmt.Errorf("failed to read DeployKey: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}
