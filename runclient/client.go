package runclient

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/uidbz/tie/io/compose"
	"github.com/uidbz/tie/io/getlib"
	"github.com/uidbz/tie/io/putlib"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/runner/api"
)

// GetFilehost asks a runner which filehost its jobs read from.
func GetFilehost(deploymanager string) (string, error) {
	filehost, _, err := GetFilehostStore(deploymanager)
	return filehost, err
}

// GetFilehostStore queries the runner's /api/filehost endpoint for both
// its filehost URL and its configured upload store. The store is
// normally set only on the runner, so clients upload to the same store
// the runner's jobs use without duplicating the setting; an empty store
// means the default store.
func GetFilehostStore(deploymanager string) (filehost, store string, err error) {
	var r api.DeployResponse
	if err := do(call{Method: http.MethodGet, Runner: deploymanager, Path: "/api/filehost"}, &r); err != nil {
		return "", "", err
	}
	if r.ErrorMsg != "" {
		return r.Message, r.Store, errors.New(r.ErrorMsg)
	}
	return r.Message, r.Store, nil
}

// CancelJob sends DELETE {runner}/api/jobs/{jobID} with Basic Auth
// (AuthUser:deployKey). The runner authorizes against the DeployKey of
// the job's handler image.
func CancelJob(runner, jobID, deployKey string) error {
	err := do(call{Method: http.MethodDelete, Runner: runner, Path: "/api/jobs/" + seg(jobID), Key: deployKey}, nil)
	if errors.Is(err, ErrUnauthorized) {
		return fmt.Errorf("%w for this job", ErrUnauthorized)
	}
	return err
}

// SetService starts (POST) or stops (DELETE) a service by name on a
// runner, authorizing with Basic Auth (AuthUser:deployKey). The runner
// controls the same resource by HTTP method: POST = start, DELETE =
// stop.
func SetService(runner, name, deployKey string, start bool) error {
	method := http.MethodDelete
	if start {
		method = http.MethodPost
	}
	err := do(call{Method: method, Runner: runner, Path: "/api/services/" + seg(name), Key: deployKey}, nil)
	if errors.Is(err, ErrUnauthorized) {
		return fmt.Errorf("%w for this service", ErrUnauthorized)
	}
	return err
}

// DeleteImage deletes a deployed image from the runner by sending
// DELETE {runner}/api/images/{name} with Basic Auth (AuthUser:deployKey).
// The runner stops the service if running, removes the container image,
// and removes the entry from its registry.
func DeleteImage(runner, name, deployKey string) error {
	err := do(call{Method: http.MethodDelete, Runner: runner, Path: "/api/images/" + seg(name), Key: deployKey}, nil)
	switch {
	case errors.Is(err, ErrUnauthorized):
		return fmt.Errorf("%w for this image", ErrUnauthorized)
	case errors.Is(err, ErrNotFound):
		return errors.New("image not found on runner")
	}
	return err
}

// ListImages lists the images deployed on one runner (public view: the
// DeployKey is never served).
func ListImages(runner string) ([]api.ImageInfo, error) {
	var out []api.ImageInfo
	if err := do(call{Method: http.MethodGet, Runner: runner, Path: "/api/images", Timeout: listTimeout}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetStatus reads one job's current record. A runner-level ErrorMsg
// (unknown job) becomes the returned error; the record is still handed
// back so callers can show what came with it.
func GetStatus(runner, jobID string) (api.JobStatus, error) {
	var r api.JobStatus
	err := do(call{Method: http.MethodGet, Runner: runner, Path: "/api/status/" + seg(jobID)}, &r)
	if err != nil {
		return r, err
	}
	if r.ErrorMsg != "" {
		return r, errors.New(r.ErrorMsg)
	}
	return r, nil
}

// CurrentUser returns the OS login name of the current user, used for
// job attribution. Falls back to $USER and finally "unknown".
func CurrentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		// On Windows Username is "DOMAIN\user"; keep just the account name.
		if i := strings.LastIndexAny(u.Username, `\/`); i >= 0 {
			return u.Username[i+1:]
		}
		return u.Username
	}
	if env := os.Getenv("USER"); env != "" {
		return env
	}
	return "unknown"
}

// Run submits one job and returns the created record. The caller owns
// waiting for it (ShowProgress, or nothing at all: the runner keeps the
// job regardless).
func Run(runner string, req api.JobRequest) (api.JobStatus, error) {
	if req.User == "" {
		req.User = CurrentUser()
	}
	var r api.JobStatus
	// Submitting can pull a large image on the runner, so it gets a longer
	// budget than the 30s default.
	if err := do(call{Method: http.MethodPost, Runner: runner, Path: "/api/run", Body: req, Timeout: 60 * time.Second}, &r); err != nil {
		return r, err
	}
	if r.ErrorMsg != "" {
		return r, errors.New(r.ErrorMsg)
	}
	return r, nil
}

// Upload sends one file or directory to the filehost and returns its
// content address (a job's InputHash).
func Upload(filehost, input, store string) (string, error) {
	// Validate before printing the banner: an error on a missing input
	// used to be glued onto the same line as "Uploading files...".
	if _, err := os.Stat(input); errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("Upload error: " + err.Error())
	}
	// Absolute path up front, so the upload is independent of the
	// caller's working directory.
	abs, err := filepath.Abs(input)
	if err != nil {
		return "", errors.New("Upload error: " + err.Error())
	}
	fmt.Print("Uploading files...")
	status := putlib.Upload(filehost, abs, putlib.PutConfig{Store: store})
	if status.ErrorMsg != "" {
		return "", errors.New("Upload error: " + status.ErrorMsg)
	}
	fmt.Println(" Done!")
	return status.LastItem.Hash, nil
}

// errNotExists is the sentinel statForUpload returns for a simply-absent
// path.
var errNotExists = errors.New("not found")

// statForUpload classifies an UploadBatch input: nil when stat
// succeeds, errNotExists when the path is absent — the documented
// "optional input" skip — and a wrapped error naming the path for every
// other stat failure (permission denied, I/O error), so a typo in a
// required path aborts instead of silently vanishing from the job.
func statForUpload(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errNotExists
		}
		return fmt.Errorf("upload input %q: %w", path, err)
	}
	return nil
}

// UploadBatch uploads each input path and ties them together under a
// single tie directory, returning that directory's hash for use as a
// job's InputHash. Inputs that do not exist are skipped (printing
// "skipped: <path> (not found)"), so callers can list optional files;
// any other stat error aborts. Two inputs that flatten to the same name
// with different content are a collision error, not a silent overwrite.
//
// With oneLevel each input is placed under its base name, which is what
// handler images expect: a flat input directory. Otherwise the input's
// own path is preserved, which requires synthesizing the interior
// directories because a tie entry names a single path component.
func UploadBatch(filehost string, input []string, oneLevel bool, store string) (string, error) {
	fmt.Print("Uploading files...")

	// putlib uploads each input under its own base name, giving the blob's
	// intrinsic content hash; where it lands in the composed tree is decided
	// here, not by putlib.
	config := putlib.PutConfig{Store: store}
	tree := compose.NewTreeBuilder()

	for _, x := range input {
		switch err := statForUpload(x); {
		case errors.Is(err, errNotExists):
			fmt.Printf("\nskipped: %s (not found)", x)
			continue // optional input that isn't there
		case err != nil:
			return "", err // permission/IO errors abort
		}
		abs, err := filepath.Abs(x)
		if err != nil {
			return "", fmt.Errorf("upload input %q: %w", x, err)
		}
		status := putlib.Upload(filehost, abs, config)
		if status.ErrorMsg != "" {
			return "", errors.New("Upload error: " + status.ErrorMsg)
		}
		name := x
		if oneLevel {
			name = filepath.Base(x)
		}
		item := status.LastItem
		if err := tree.Add(name, metadata.DirEntry{
			Hash: item.Hash,
			Size: item.Size,
			Head: item.Head,
		}); err != nil {
			return "", err
		}
	}

	hash, err := tree.Upload(filehost, store)
	if err != nil {
		return "", err
	}
	fmt.Println(" Done!")

	return hash, nil
}

// Download fetches a content address (typically a job's OutputHash) from
// the filehost into destination.
func Download(filehost, hash, destination string) error {
	// Destination made absolute up front, independent of any
	// working-directory changes elsewhere.
	abs, err := filepath.Abs(destination)
	if err != nil {
		return errors.New("Error downloading output: " + err.Error())
	}
	fmt.Print("Downloading files...")

	err = getlib.DownloadFile(http.DefaultClient, filehost, hash, abs, nil)

	if err != nil {
		return errors.New("Error downloading output: " + err.Error() + "\nFilehost:" + filehost + "\nHash:" + hash + "\nDestination:" + abs)
	}

	fmt.Println(" Done!")
	// The absolute output path is printed by the callers (e.g. the
	// deploy flow prints "Test output: ..."); printing it here too
	// showed it twice.

	return nil
}

// StartRun submits a job and watches it to completion, printing the
// progress display. The headless form prints only percent transitions
// (for scripts).
func StartRun(runner string, headless bool, req api.JobRequest) (api.JobStatus, error) {
	return StartRunCtx(context.Background(), runner, headless, req)
}

// StartRunCtx is StartRun with a context threaded into the progress
// watch, so a cancelled context (Ctrl-C in the CLI) stops the wait
// cleanly. StartRun stays for callers without a context.
func StartRunCtx(ctx context.Context, runner string, headless bool, req api.JobRequest) (api.JobStatus, error) {
	resp, err := Run(runner, req)
	if err != nil {
		return api.JobStatus{}, err
	}

	status, err := ShowProgressCtx(ctx, runner, resp.JobID, headless, true)
	if err != nil {
		return api.JobStatus{}, err
	}
	fmt.Println("RunningTime:", status.RunningTime)

	if req.Debug {
		fmt.Println("Log:")
		fmt.Println(status.Log)
		if status.ErrorMsg != "" {
			fmt.Println("Error message:")
			fmt.Println(status.ErrorMsg)
		}
		fmt.Println("Runner:", runner)
		fmt.Println("JobType:", status.JobType)
		fmt.Println("Image:", status.Handler)
		fmt.Println("InputHash:", status.InputHash)
		fmt.Println("OutputHash:", status.OutputHash)
		fmt.Println("Injected App Hash:", status.AppHash)
		fmt.Println("Settings:", string(req.Settings))
		// RunningTime already printed above; printing it twice in debug
		// mode was noise.
	}

	return status, nil
}

// pollBackoff tracks transient-failure backoff for the polling loops: a
// runner restart or Wi-Fi blip must not kill a watch of a job that keeps
// running. Up to pollMaxConsecutiveErrors consecutive fetch failures are
// tolerated with exponential backoff from pollBackoffStart to
// pollBackoffMax; any success resets the counter. Vars (not consts) so
// tests can shrink the waits without sleeping for minutes; production
// defaults follow the 1s -> 30s envelope.
var (
	pollMaxConsecutiveErrors = 10
	pollBackoffStart         = 1 * time.Second
	pollBackoffMax           = 30 * time.Second
)

type pollBackoff struct {
	consecutive int
	delay       time.Duration
}

// failure waits out the backoff for one failed fetch and reports whether
// the consecutive-failure budget is exhausted. ctx cancellation ends the
// wait early and counts as "stop watching" (returns true with a nil
// error).
func (b *pollBackoff) failure(ctx context.Context) (stop bool, err error) {
	b.consecutive++
	if b.consecutive >= pollMaxConsecutiveErrors {
		return true, nil
	}
	if b.delay == 0 {
		b.delay = pollBackoffStart
	} else {
		b.delay *= 2
		if b.delay > pollBackoffMax {
			b.delay = pollBackoffMax
		}
	}
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-time.After(b.delay):
		return false, nil
	}
}

// success resets the counter after a good fetch.
func (b *pollBackoff) success() {
	b.consecutive = 0
	b.delay = 0
}

// sleepCtx waits d, returning false if ctx is done first. Keeps the
// success-path poll rate interruptible.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ShowProgress watches one job, rendering progress, until it finishes.
func ShowProgress(runner, jobID string, headless, follow bool) (api.JobStatus, error) {
	return ShowProgressCtx(context.Background(), runner, jobID, headless, follow)
}

// ShowProgressCtx watches a job with a context: on cancellation it
// prints how to resume watching and returns nil — the job keeps running
// on the runner, so a cancelled watch is benign, not a failure.
// Transient status errors are tolerated per pollBackoff; after
// pollMaxConsecutiveErrors consecutive failures it returns an error
// naming the runner and the resume command instead of aborting on the
// first blip.
func ShowProgressCtx(ctx context.Context, runner, jobID string, headless, follow bool) (api.JobStatus, error) {
	fmt.Println("JobID:", jobID)
	runningShowed := false
	waitingShowed := false
	done := false
	var lastPct int
	var status api.JobStatus
	var bar *progressbar.ProgressBar
	if !headless {
		bar = progressbar.Default(100)
	}
	var backoff pollBackoff
	for !done {
		select {
		case <-ctx.Done():
			fmt.Printf("watching cancelled; job %s still running on %s — resume with %s status %s\n", jobID, runner, CLIName, jobID)
			return status, nil
		default:
		}
		var err error
		status, err = GetStatus(runner, jobID)
		if err != nil {
			stop, ctxErr := backoff.failure(ctx)
			if ctxErr != nil {
				fmt.Printf("watching cancelled; job %s still running on %s — resume with %s status %s\n", jobID, runner, CLIName, jobID)
				return status, nil
			}
			if stop {
				return status, fmt.Errorf("job %s is still on %s; check with %s status %s", jobID, runner, CLIName, jobID)
			}
			continue
		}
		backoff.success()
		done = status.Done
		if !headless {
			bar.Set(int(status.Percent))
		} else {
			if lastPct != int(status.Percent) {
				lastPct = int(status.Percent)
				fmt.Println(status.Percent)
			}
		}
		if !follow {
			return status, nil
		}
		if status.IsRunning {
			if !runningShowed {
				if !headless {
					bar.Clear()
				}
				fmt.Println("The job is now running!")
				runningShowed = true
				if !headless {
					bar.RenderBlank()
				}
			}
		} else {
			if !waitingShowed {
				if !headless {
					bar.Clear()
				}
				fmt.Println("Waiting for other jobs to finish...")
				waitingShowed = true
				if !headless {
					bar.RenderBlank()
				}
			}
		}
		if !sleepCtx(ctx, 500*time.Millisecond) {
			fmt.Printf("watching cancelled; job %s still running on %s — resume with %s status %s\n", jobID, runner, CLIName, jobID)
			return status, nil
		}
	}
	if !headless {
		bar.Set(100)
	}
	return status, nil
}
