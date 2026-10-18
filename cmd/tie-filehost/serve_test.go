package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeBuf collects log records from several goroutines; the serve tests read
// what the server logs while the server is still writing it.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureSlog points the default logger at w for the duration of the test.
func captureSlog(t *testing.T, w io.Writer) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// TestServeBindsBeforeItClaimsListening is the guard for the filehost's own
// serve path. http.ListenAndServe logs and then binds, so with the port already
// taken — the classic upgrade mistake, the old filehost still holding 1162
// while the new one is restarted every few seconds — the log said "listening"
// and every connection was refused. The log line may only appear once the
// socket exists.
func TestServeBindsBeforeItClaimsListening(t *testing.T) {
	logs := &safeBuf{}
	captureSlog(t, logs)

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	err = serve(http.NewServeMux(), FilehostConfig{ListenOn: busy.Addr().String(), Insecure: true})
	if err == nil {
		t.Fatal("serve() reported no error on a port already in use")
	}
	if !strings.Contains(err.Error(), "listening on") {
		t.Errorf("the error does not name the address it failed on: %v", err)
	}
	if strings.Contains(logs.String(), "msg=listening") {
		t.Errorf("the log announced a server that was never bound:\n%s", logs.String())
	}
}

// TestServeRefusesHTTPSWithoutCerts: the config error is reported before any
// socket is opened, and nothing is announced.
func TestServeRefusesHTTPSWithoutCerts(t *testing.T) {
	logs := &safeBuf{}
	captureSlog(t, logs)

	err := serve(http.NewServeMux(), FilehostConfig{ListenOn: "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "Insecure") {
		t.Fatalf("serve() without certificates: got %v, want the Insecure/CertFile guidance", err)
	}
	if strings.Contains(logs.String(), "msg=listening") {
		t.Errorf("the log announced a server that was never bound:\n%s", logs.String())
	}
}

// TestServeAnswersOnceItSaysListening is the other half: the line is not just
// honest, it is a promise. Whatever address it names must answer.
func TestServeAnswersOnceItSaysListening(t *testing.T) {
	logs := &safeBuf{}
	captureSlog(t, logs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})
	go func() {
		if err := serve(mux, FilehostConfig{ListenOn: "127.0.0.1:0", Insecure: true}); err != nil {
			t.Error(err)
		}
	}()

	addr := ""
	deadline := time.Now().Add(5 * time.Second)
	for addr == "" && time.Now().Before(deadline) {
		for _, line := range strings.Split(logs.String(), "\n") {
			if i := strings.Index(line, "addr=http://"); i >= 0 {
				addr = strings.TrimSpace(line[i+len("addr=http://"):])
			}
		}
		if addr == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if addr == "" {
		t.Fatalf("serve() never logged an address:\n%s", logs.String())
	}

	resp, err := http.Get("http://" + addr + "/ping")
	if err != nil {
		t.Fatalf("the address in the log (%s) does not answer: %v", addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /ping = %s", resp.Status)
	}
}
