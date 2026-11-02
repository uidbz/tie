// Package deploy is the image-build side of a runner: fetch a project
// tarball from the filehost, build it into a tagged image, and push it to
// the registry. It knows nothing about job scheduling or the HTTP layer;
// the runner wires it into the deploy endpoint and upserts the resulting
// image into its registry.
package deploy

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/uidbz/tie/io/getlib"
)

// Builder runs the container CLI. Bin is configurable so deployments can
// point at a wrapper and tests can fake it. Registry is the tag prefix
// (the Docker registry every deploy pushes to); Filehost and BuildArgs
// are accessors so a hot-reloaded config takes effect on the next build.
type Builder struct {
	Bin       string
	Registry  func() string
	Filehost  func() string
	BuildArgs func() map[string]string
	Client    *http.Client
}

func (b *Builder) bin() string {
	if b.Bin == "" {
		return "docker"
	}
	return b.Bin
}

func (b *Builder) tag(name string) string {
	reg := ""
	if b.Registry != nil {
		reg = b.Registry()
	}
	if reg == "" {
		return name
	}
	return reg + "/" + name
}

// DownloadProject fetches a project tarball by content hash into a temp
// directory keyed by the hash (the hash IS the identity: a re-deploy of
// the same sources reuses the download).
func (b *Builder) DownloadProject(hash string) (string, error) {
	dest := filepath.Join(os.TempDir(), "tie-deploy", hash)
	filehost := ""
	if b.Filehost != nil {
		filehost = b.Filehost()
	}
	client := b.Client
	if client == nil {
		client = http.DefaultClient
	}
	err := getlib.DownloadFile(client, filehost, hash, dest, nil)
	return dest, err
}

// Build builds name from path ("docker build --pull [args] path -t
// reg/name") and returns the CLI output for the deployer's transcript.
func (b *Builder) Build(name, path string, noCache, debug bool) (string, error) {
	tag := b.tag(name)
	slog.Info("Building Docker image", "name", name, "tag", tag, "no_cache", noCache)
	args := []string{"build", "--pull"}
	if noCache {
		args = append(args, "--no-cache")
	}
	if b.BuildArgs != nil {
		for key, val := range b.BuildArgs() {
			args = append(args, "--build-arg", key+"="+val)
		}
	}
	args = append(args, path, "-t", tag)
	if debug {
		slog.Debug("Docker build command", "args", append([]string{b.bin()}, args...))
	}

	out, err := exec.Command(b.bin(), args...).CombinedOutput()
	if err != nil {
		slog.Error("Docker build failed", "name", name, "error", err, "output", string(out))
		return "", errors.New(string(out) + "\n" + err.Error())
	}
	slog.Info("Docker build completed", "name", name)
	return string(out), nil
}

// Push publishes the built tag to the registry.
func (b *Builder) Push(name string) (string, error) {
	tag := b.tag(name)
	slog.Info("Pushing Docker image", "name", name, "tag", tag)
	out, err := exec.Command(b.bin(), "push", tag).CombinedOutput()
	if err != nil {
		slog.Error("Docker push failed", "name", name, "error", err)
		return "", fmt.Errorf("%s\n%s", string(out), err.Error())
	}
	slog.Info("Docker push completed", "name", name)
	return string(out), nil
}

// RemoveLocal deletes the local copy of a deployed image after the
// registry entry is dropped. Failure is not fatal (the image may simply
// not exist locally), so the caller only logs it.
func (b *Builder) RemoveLocal(name string) (string, error) {
	tag := b.tag(name)
	slog.Info("Removing Docker image", "tag", tag)
	out, err := exec.Command(b.bin(), "rmi", tag).CombinedOutput()
	return string(out), err
}
