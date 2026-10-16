package tiedb

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

// TestMain quiets the store's INFO logging: one line per request and per
// collection load is thousands of lines of noise around the failures that
// matter, and no test in this package asserts on log output.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})))
	os.Exit(m.Run())
}
