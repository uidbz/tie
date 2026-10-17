package main

import (
	"io"
	"os"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

// The transfer bars are for a person watching a terminal. Written to a log, a
// pipe or cron they are thousands of carriage-return frames that say nothing and
// push the useful lines off the screen, so they only render when stderr is a
// terminal — and `-q` silences them even there (§6.8).

// progress is the sink a transfer reports its bytes to: a bar on stderr when
// someone is watching, a counter that goes nowhere otherwise. It implements
// io.Writer, which is what the upload and download helpers take.
type progress struct {
	bar *progressbar.ProgressBar
}

// newProgress builds the bar for a transfer of total bytes (unknown when total
// is negative), or the silent stand-in.
func newProgress(ctx *cli.Command, total int64, label string) *progress {
	if !showProgress(ctx) {
		return &progress{}
	}
	return &progress{bar: progressbar.NewOptions64(
		total,
		progressbar.OptionSetDescription(label),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionClearOnFinish(),
		// Without a throttle the bar re-renders the terminal line on
		// every write it receives — one per streamed chunk — which for a
		// multi-GB upload is millions of renders and throttles transfer
		// to a few MB/s. Cap redraws to ~60fps; transfer stays wire-speed.
		progressbar.OptionThrottle(16*time.Millisecond),
	)}
}

// showProgress decides whether a bar is worth drawing at all.
func showProgress(ctx *cli.Command) bool {
	return progressWanted(ctx.Bool("quiet"), term.IsTerminal(int(os.Stderr.Fd())))
}

// progressWanted is the decision, apart from the terminal check so it can be
// tested both ways (§6.8).
func progressWanted(quiet, stderrIsTerminal bool) bool {
	return stderrIsTerminal && !quiet
}

// Write counts the bytes transferred. Without a bar there is nothing to render,
// but the transfer still has to see its writes succeed.
func (p *progress) Write(b []byte) (int, error) {
	if p.bar == nil {
		return len(b), nil
	}
	return p.bar.Write(b)
}

// Finish closes the bar and clears its line. Safe on the silent stand-in, so the
// call sites do not have to know which one they got.
func (p *progress) Finish() {
	if p.bar != nil {
		p.bar.Finish()
	}
}

var _ io.Writer = (*progress)(nil)
