package getlib

// Tests for #114: the download and read paths take an *http.Client, so a caller
// could already bound them with a client Timeout — but it could not *cancel* a
// transfer it had lost interest in (a GUI changing page, an aborted export).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stallingServer sends the beginning of a body and then holds the connection
// open: the transfer is real and in flight, and only a caller-side cancellation
// can end it.
func stallingServer(content string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Write([]byte(content[:16]))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	}))
}

func TestContextVariantsRefuseCancelledCallers(t *testing.T) {
	content := strings.Repeat("payload bytes for a transfer nobody wants any more ", 256)
	hash := hashOf(t, content)
	srv := stallingServer(content)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ReadBytesContext(ctx, nil, srv.URL, hash); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadBytesContext with a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := ReadFileContext(ctx, nil, srv.URL, hash); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFileContext = %v, want context.Canceled", err)
	}
	if _, err := TotalSizeContext(ctx, nil, srv.URL, hash); !errors.Is(err, context.Canceled) {
		t.Errorf("TotalSizeContext = %v, want context.Canceled", err)
	}
	if err := DownloadFileContext(ctx, nil, srv.URL, hash, filepath.Join(t.TempDir(), "out.txt"), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("DownloadFileContext = %v, want context.Canceled", err)
	}
}

// TestCancelledDownloadLeavesNothingBehind pairs with the atomic-staging tests:
// the cancellation has to stop the transfer *and* clean up what it had staged.
func TestCancelledDownloadLeavesNothingBehind(t *testing.T) {
	content := strings.Repeat("bytes still on their way to a caller that gave up ", 4096)
	hash := hashOf(t, content)
	srv := stallingServer(content)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")
	if err := DownloadFileContext(ctx, nil, srv.URL, hash, dest, nil); err == nil {
		t.Error("a download cancelled mid-body returned no error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("the cancelled download left a file at %s", dest)
	}
	if left := onlyFiles(t, dir); len(left) != 0 {
		t.Errorf("the cancelled download left files behind: %v", keys(left))
	}
}
