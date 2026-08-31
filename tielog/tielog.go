// Package tielog installs the process-wide slog logger used by tie-triplestore and
// tie-filehost. It fans every record out to two sinks at once: structured JSON
// appended to a log file (for machine parsing / retention) and a pretty,
// optionally colored line on stderr (for a human watching the console). stdout
// is never touched — it is reserved for data (e.g. dump TSV).
package tielog

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// Config controls logger setup. The zero value is valid: no file, info level,
// pretty stderr.
type Config struct {
	// File is the path to append JSON logs to. Empty disables file logging.
	File string
	// Level is the minimum level to emit ("debug", "info", "warn", "error").
	// Empty defaults to info.
	Level string
}

// Setup installs a slog default logger per cfg and returns a cleanup func that
// closes the log file (a no-op if none was opened). Call it once at startup;
// any error opening the file is returned but stderr logging is still installed.
func Setup(cfg Config) (cleanup func(), err error) {
	level := parseLevel(cfg.Level)
	handlers := []slog.Handler{newPrettyHandler(os.Stderr, level)}
	cleanup = func() {}

	if cfg.File != "" {
		f, ferr := os.OpenFile(cfg.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
		if ferr != nil {
			err = ferr
		} else {
			handlers = append(handlers, slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level}))
			cleanup = func() { f.Close() }
		}
	}

	slog.SetDefault(slog.New(&fanout{handlers: handlers}))
	return cleanup, err
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// fanout broadcasts each record to every wrapped handler. WithAttrs/WithGroup
// are propagated so derived loggers keep fanning out.
type fanout struct{ handlers []slog.Handler }

func (f *fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f *fanout) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &fanout{handlers: next}
}

func (f *fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &fanout{handlers: next}
}

// newPrettyHandler returns the human-facing stderr handler. When stderr is a
// terminal it colors the level tag; otherwise (a pipe or redirect) it falls
// back to slog's plain TextHandler so captured logs stay clean and parseable.
func newPrettyHandler(w *os.File, level slog.Level) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if !term.IsTerminal(int(w.Fd())) {
		return slog.NewTextHandler(w, opts)
	}
	return &prettyHandler{w: w, level: level, mu: &sync.Mutex{}}
}
