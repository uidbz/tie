package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/tiedb"
	"github.com/uidbz/tie/webservice"
)

// localBackend owns one in-process database directory: the webservice that
// serves it, the transport the client talks through, and the exclusive
// per-collection locks the engine takes while a collection is loaded.
type localBackend struct {
	dir      string
	readOnly bool
	// snapshot is a private copy of dir's .tie files taken at open when the
	// backend is read-only (#147). Reads are served from it, so a live
	// writer cannot change the answer under the reader; Close removes it.
	// dir keeps the real location for sibling scoping.
	snapshot  string
	transport *localTransport
	closeOnce sync.Once
}

// openLocalBackend validates the collection file and starts the in-process
// service. The file itself may be absent: a sibling collection (e.g. -c Other
// or the <name>_prev history collection) is created on its first write, exactly
// as it is on a server — but when it exists it must be a regular file, so a
// directory or FIFO named X.tie is rejected rather than opened or hung on
// (#152). reverse is the direct-mode reverse-index subset (empty = all);
// readOnly opens without a writer or lock, served from a load-time snapshot of
// the directory (#147).
func openLocalBackend(file string, reverse []string, readOnly bool) (*localBackend, error) {
	dir, exists, err := collectionFileDir(file)
	if err != nil {
		return nil, err
	}
	b := &localBackend{dir: dir, readOnly: readOnly}
	if readOnly && exists {
		snap, serr := snapshotCollectionDir(dir)
		if serr != nil {
			return nil, fmt.Errorf("read-only snapshot of %s: %w", dir, serr)
		}
		b.snapshot = snap
	}
	serve := dir
	if b.snapshot != "" {
		serve = b.snapshot
	}
	b.transport = newLocalTransport(serve, reverse, readOnly)
	return b, nil
}

// Close flushes every loaded collection, releases its lock, and removes the
// read-only snapshot if there was one. Safe to call repeatedly.
func (b *localBackend) Close() {
	b.closeOnce.Do(func() {
		b.transport.Close()
		if b.snapshot != "" {
			_ = os.RemoveAll(b.snapshot)
		}
	})
}

// ConfigForFile returns a config that opens path directly, with no config file
// required. Filehosts and import templates can still be merged in by the
// caller (the CLI does). An offline open gets NO filehosts: contacting an
// implicit localhost host instead of failing "no filehost configured" would
// contradict the README and surprise the user (#151).
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
	c.FileHosts = map[string]FileHost{}
	c.DefaultFileHosts = nil
	return c, nil
}

// checkCollectionShape validates the NAME of a direct-mode collection file:
// the .tie suffix and a collection-safe basename (#152).
func checkCollectionShape(file string) error {
	if !strings.HasSuffix(file, ".tie") {
		return fmt.Errorf("%q is not a .tie database file", file)
	}
	if base := strings.TrimSuffix(filepath.Base(file), ".tie"); !webservice.ValidCollectionName(base) {
		return fmt.Errorf("%q is not a valid collection file name", file)
	}
	return nil
}

// canonicalCollectionFile is the one collection-file validator shared by every
// direct open path (#152): name shape, absolute path, and — when the file
// already exists — symlink resolution to the target's identity plus a regular
// file check. A missing sibling is allowed (exists=false); an existing
// directory, FIFO or other non-regular path is rejected. Canonicalizing an
// existing primary here is what makes a symlinked TripleStoreFile keep the
// TARGET's basename and put <name>_prev beside the target, not beside the
// link (#151).
func canonicalCollectionFile(file string) (abs string, exists bool, err error) {
	if err = checkCollectionShape(file); err != nil {
		return "", false, err
	}
	abs, err = filepath.Abs(file)
	if err != nil {
		return "", false, err
	}
	if real, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = real
		exists = true
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return "", false, rerr
	}
	if exists {
		fi, serr := os.Stat(abs)
		if serr != nil {
			return "", false, serr
		}
		if !fi.Mode().IsRegular() {
			return "", false, fmt.Errorf("%q is not a regular file", abs)
		}
	}
	return abs, exists, nil
}

// ValidateCollectionFile is the exported form of the shared validator, used
// by embedders that construct a direct-mode config themselves (tie-smb).
// It returns the canonical path and whether the file already exists.
func ValidateCollectionFile(path string) (string, bool, error) {
	return canonicalCollectionFile(path)
}

// collectionFileParts is the full decomposition of a direct-mode file: the
// canonical file path (#152 rules), the directory collections resolve
// against, and whether the file exists yet.
func collectionFileParts(file string) (abs, dir string, exists bool, err error) {
	abs, exists, err = canonicalCollectionFile(file)
	if err != nil {
		return "", "", false, err
	}
	dir = filepath.Dir(abs)
	if real, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		dir = real
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", "", false, err
	}
	if !fi.IsDir() {
		return "", "", false, fmt.Errorf("%q is not a directory", dir)
	}
	return abs, dir, exists, nil
}

// collectionFileDir validates the file with the shared rules and resolves the
// directory it lives in. The file may be absent (a sibling created on first
// write); the directory must exist.
func collectionFileDir(file string) (dir string, exists bool, err error) {
	_, dir, exists, err = collectionFileParts(file)
	return dir, exists, err
}

// existingTieFile validates the database file a `-f` open names. A missing
// file is an error rather than an empty new database: a mistyped -f must not
// look like it worked.
func existingTieFile(path string) (string, error) {
	abs, exists, err := canonicalCollectionFile(path)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("stat %s: %w", abs, os.ErrNotExist)
	}
	return abs, nil
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

// DumpLocalFile streams every forward triple of the on-disk collection at
// path without a server. It is the one local-file reader shared with the
// direct-mode client (#155): the same validation every direct open runs
// (#152), the mandatory hash-blob policy, and — because a dump only walks
// forward triples — NO reverse index, which halves load time on a large
// database and is what docs/internals.md promises for `dump --file` (#148).
// readOnly opens without the exclusive lock, so the export can run beside a
// live server; otherwise the lock makes "stop the server first" a hard error
// rather than advice.
func DumpLocalFile(path string, readOnly bool, write func(tiedb.StringTriple) error) error {
	abs, dir, exists, err := collectionFileParts(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("stat %s: %w", abs, os.ErrNotExist)
	}
	name := strings.TrimSuffix(filepath.Base(abs), ".tie")
	db := tiedb.NewDB(true)
	db.SetLocking(!readOnly)
	db.SetReadOnly(readOnly)
	db.SetDefaultReverseRelations([]string{}) // explicit empty = none, not "all"
	db.SetBlobPolicy(tiedb.HexHashBlobPolicy(metadata.IsHexHash))
	defer db.Close()
	col := db.GetCollection(tiedb.CollectionKey{Database: dir, Collection: name})
	if err := col.Err(); err != nil {
		return err
	}
	var writeErr error
	col.ForEachTriple(func(t tiedb.StringTriple) {
		if writeErr != nil {
			return
		}
		writeErr = write(t)
	})
	return writeErr
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
// collection and any sibling collection all work. An empty reverse list means
// "index everything": TOML decodes `ReverseRelations = []` as a non-nil empty
// slice, which tiedb would read as an empty index; nil is the "all" signal, so
// normalize here, the one constructor every direct open passes through (#151).
func newLocalTransport(dir string, reverse []string, readOnly bool) *localTransport {
	if len(reverse) == 0 {
		reverse = nil
	}
	ws := webservice.NewWebservice(webservice.WebserviceConfig{
		LocalDir:         dir,
		ReverseRelations: reverse,
		ReadOnly:         readOnly,
	})
	api.RegisterAll(ws)
	return &localTransport{ws: ws}
}

func (t *localTransport) Do(ctx context.Context, id string, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	status := t.ws.Dispatch(id, body, &buf)
	// A handler already running cannot be interrupted mid-call — tiedb's
	// walks are not interruptible — but HTTP at least discards the reply when
	// the request's context is done. Report cancellation after the fact too,
	// so a timeout really bounds the caller (#149).
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
