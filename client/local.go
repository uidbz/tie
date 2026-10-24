package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/webservice"
)

// localBackend owns one in-process database directory: the webservice that
// serves it, the transport the client talks through, and the exclusive
// per-collection locks the engine takes while a collection is loaded.
type localBackend struct {
	dir       string
	transport *localTransport
	closeOnce sync.Once
}

// openLocalBackend validates the collection file's directory and starts the
// in-process service. The file itself may be absent: a sibling collection
// (e.g. -c Other or the <name>_prev history collection) is created on its first
// write, exactly as it is on a server.
func openLocalBackend(file string) (*localBackend, error) {
	dir, err := localCollectionDir(file)
	if err != nil {
		return nil, err
	}
	return &localBackend{dir: dir, transport: newLocalTransport(dir)}, nil
}

// Close flushes every loaded collection and releases its lock. Safe to call
// repeatedly.
func (b *localBackend) Close() {
	b.closeOnce.Do(func() { b.transport.Close() })
}

// ConfigForFile returns a config that opens path directly, with no config file
// required. Filehosts and import templates can still be merged in by the
// caller (the CLI does).
func ConfigForFile(path string) (Config, error) {
	abs, err := existingTieFile(path)
	if err != nil {
		return Config{}, err
	}
	c := defaultConfig
	c.TripleStoreURL = ""
	c.Namespace = ""
	c.TripleStoreFile = abs
	c.Collection = strings.TrimSuffix(filepath.Base(abs), ".tie")
	c.DefaultCollection = c.Collection
	c.Collections = nil
	// defaultConfig's FileHosts is a package-level map; copy it so a caller that
	// edits this config cannot mutate the default for every later client.
	c.FileHosts = make(map[string]FileHost, len(defaultConfig.FileHosts))
	for name, host := range defaultConfig.FileHosts {
		c.FileHosts[name] = host
	}
	return c, nil
}

// existingTieFile canonicalizes and validates the database file a `-f` open
// names. A missing file is an error rather than an empty new database: a
// mistyped -f must not look like it worked.
func existingTieFile(path string) (string, error) {
	if !strings.HasSuffix(path, ".tie") {
		return "", fmt.Errorf("%q is not a .tie database file", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = real
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", abs)
	}
	if base := strings.TrimSuffix(filepath.Base(abs), ".tie"); !webservice.ValidCollectionName(base) {
		return "", fmt.Errorf("%q is not a valid collection file name", abs)
	}
	return abs, nil
}

// localCollectionDir resolves the directory a direct-mode collection lives in.
// The collection file itself may not exist yet, so only the directory is
// checked.
func localCollectionDir(file string) (string, error) {
	if !strings.HasSuffix(file, ".tie") {
		return "", fmt.Errorf("%q is not a .tie database file", file)
	}
	if base := strings.TrimSuffix(filepath.Base(file), ".tie"); !webservice.ValidCollectionName(base) {
		return "", fmt.Errorf("%q is not a valid collection file name", file)
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	if real, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		dir = real
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%q is not a directory", dir)
	}
	return dir, nil
}

// localDirFor is the comparison form of localCollectionDir: two collections are
// siblings when their directories resolve to the same place.
func localDirFor(file string) string {
	dir := filepath.Dir(file)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// localTransport serves the tie protocol from an in-process Webservice backed
// by one on-disk database directory. No socket is opened and no credentials
// are checked: the .tie file's filesystem permissions are the access boundary.
//
// It exists so the whole CLI, the mounts and the tests can keep using the same
// TieClient façade: every request still runs through api.RegisterAll, so query
// and write semantics cannot drift from a real server's.
type localTransport struct {
	ws *webservice.Webservice
}

// newLocalTransport opens the in-process service for dir. Collections resolve
// to <dir>/<id>.tie, so the primary collection, the <name>_prev history
// collection and any sibling collection all work.
func newLocalTransport(dir string) *localTransport {
	ws := webservice.NewWebservice(webservice.WebserviceConfig{LocalDir: dir})
	api.RegisterAll(ws)
	return &localTransport{ws: ws}
}

func (t *localTransport) Do(ctx context.Context, id string, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	status := t.ws.Dispatch(id, body, &buf)
	if status < 200 || status >= 300 {
		return nil, localStatusError(id, status, buf.Bytes())
	}
	return buf.Bytes(), nil
}

func (t *localTransport) DoStream(ctx context.Context, id string, body []byte, consume func(io.Reader) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	type result struct {
		started bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		started, err := t.ws.DispatchStream(ctx, id, body, pw)
		// Closing with the error both propagates a pre-body failure to consume
		// and unblocks it when the producer finished.
		_ = pw.CloseWithError(err)
		done <- result{started: started, err: err}
	}()

	err := consume(pr)
	// A consumer that stops early (broken pipe, cancelled walk) must unblock
	// the producer rather than park it on a full pipe.
	_ = pr.CloseWithError(err)

	res := <-done
	switch {
	case !res.started && res.err != nil:
		// The handler failed before writing: this is the honest error, not the
		// pipe's echo of it or a decode failure on an error body.
		return res.err
	case err != nil:
		return err
	default:
		return res.err
	}
}

// SetTimeout is a no-op: there is no network round trip to bound. Long loads
// are bounded by the caller's context where the API offers one.
func (t *localTransport) SetTimeout(time.Duration) {}

// Close flushes and closes every collection's writer. Call it once the owning
// TieClient is done with the database.
func (t *localTransport) Close() {
	t.ws.Close()
}

// localStatusError turns a non-2xx in-process answer into the same error shape
// the HTTP transport returns, so callers (and ErrNotFound mapping) behave
// identically in both modes.
func localStatusError(id string, status int, body []byte) error {
	se := &webservice.StatusError{Status: status, Request: id}
	var st webservice.ReplyStatus
	if err := json.Unmarshal(body, &st); err == nil && st.Message != "" {
		se.Message = st.Message
	} else {
		s := strings.TrimSpace(string(body))
		if len(s) > 200 {
			s = s[:200] + "..."
		}
		se.Body = s
	}
	return se
}
