package tielog

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// prettyHandler renders records as a compact, colored, human-readable line for
// an interactive terminal: `HH:MM:SS LEVEL message key=value ...`. It is used
// only when stderr is a TTY; piped output uses slog's TextHandler instead.
type prettyHandler struct {
	w      io.Writer
	level  slog.Level
	mu     *sync.Mutex
	attrs  []slog.Attr
	groups []string
}

const (
	colReset = "\033[0m"
	colDim   = "\033[2m"
)

func levelTag(l slog.Level) (text, color string) {
	switch {
	case l >= slog.LevelError:
		return "ERROR", "\033[31m" // red
	case l >= slog.LevelWarn:
		return "WARN ", "\033[33m" // yellow
	case l >= slog.LevelInfo:
		return "INFO ", "\033[32m" // green
	default:
		return "DEBUG", "\033[36m" // cyan
	}
}

func (h *prettyHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	tag, color := levelTag(r.Level)

	var b []byte
	b = append(b, colDim...)
	b = append(b, r.Time.Format(time.TimeOnly)...)
	b = append(b, colReset...)
	b = append(b, ' ')
	b = append(b, color...)
	b = append(b, tag...)
	b = append(b, colReset...)
	b = append(b, ' ')
	b = append(b, r.Message...)

	appendAttr := func(a slog.Attr) {
		if a.Equal(slog.Attr{}) {
			return
		}
		b = append(b, ' ')
		b = append(b, colDim...)
		b = append(b, a.Key...)
		b = append(b, '=')
		b = append(b, colReset...)
		b = append(b, quoteValue(a.Value.String())...)
	}
	for _, a := range h.attrs {
		appendAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(a)
		return true
	})
	b = append(b, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(b)
	return err
}

func (h *prettyHandler) prefixed(key string) string {
	if len(h.groups) == 0 {
		return key
	}
	out := ""
	for _, g := range h.groups {
		out += g + "."
	}
	return out + key
}

// quoteValue quotes a value only when it contains whitespace or quotes, keeping
// the common case (single tokens like hashes and paths) unquoted and scannable.
func quoteValue(s string) string {
	if s == "" {
		return `""`
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == ' ' || c == '"' || c == '\n' || c == '\t' {
			return strconv.Quote(s)
		}
	}
	return s
}

func (h *prettyHandler) clone() *prettyHandler {
	c := *h
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	c.groups = append([]string(nil), h.groups...)
	return &c
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := h.clone()
	// Resolve each key against the current group stack once, at attach time, so
	// stored attrs are not re-prefixed on every Handle.
	for _, a := range attrs {
		a.Key = c.prefixed(a.Key)
		c.attrs = append(c.attrs, a)
	}
	return c
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := h.clone()
	c.groups = append(c.groups, name)
	return c
}
