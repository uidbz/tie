package compose

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"time"

	"github.com/uidbz/tie/metadata"
)

type DirItem struct {
	URL       string
	Name      string
	Path      string
	MediaType string
	Hash      string
	Size      int
	Head      []byte
	Children  []*DirItem
}

// maxDirDepth bounds directory recursion. The filehost is untrusted and a
// manifest can name an ancestor as its own child, which would otherwise loop
// forever. Matches getlib's limit.
const maxDirDepth = 128

// maxDirNodes bounds the total number of items one walk materialises. Depth
// alone does not bound work: a manifest DAG that fans out wide — legitimately
// shared subtrees, or a hostile manifest — is exponential without memoisation,
// and even with it a wide enough tree is unbounded (review R3).
const maxDirNodes = 1_000_000

// errManifestTooLarge is returned when a walk exceeds its node budget.
var errManifestTooLarge = errors.New("manifest too large (>1e6 nodes)")

// httpClient gives every filehost read a deadline. http.Get used
// http.DefaultClient, which has no timeout: a stalled filehost would hang a
// script — and, because this package is shared with the runner, a pipeline
// step — forever (review R3).
var httpClient = &http.Client{Timeout: 30 * time.Second}

// DirTree walks the tie directory at sourceHash and returns a single item whose
// Children are its contents. Only directory manifests are fetched: a tie entry
// carries its child's leading bytes inline, so files are classified from their
// parent's manifest without transferring any file content.
//
// Each unique manifest is fetched at most once per call (memoised by hash), and
// the walk aborts with an error once it materialises more than maxDirNodes
// items, so a DAG — shared subtrees or a cycle — costs linear work, not
// exponential (review R3).
func DirTree(url string, sourceHash string, relPath string) ([]*DirItem, error) {
	return newDirWalker(url, maxDirNodes).tree(sourceHash, relPath)
}

// dirWalker carries the per-walk state that keeps a DAG walk linear: fetched
// manifests are memoised by hash and the total item count is budgeted. The
// exported DirTree signature stays stable for the runner; the cache lives here.
type dirWalker struct {
	url   string
	limit int
	nodes int
	// cache maps a directory hash to the children parsed from its manifest,
	// together with the base path those children's Path fields were built
	// under. The same subtree under a different base reuses the fetch but
	// clones the items so Path stays correct for selection.
	cache map[string]cachedDir
}

type cachedDir struct {
	base  string
	items []*DirItem
}

func newDirWalker(url string, limit int) *dirWalker {
	return &dirWalker{url: url, limit: limit, cache: make(map[string]cachedDir)}
}

// dirTreeWithBudget is the test seam for maxDirNodes: DirTree with a
// caller-chosen node limit instead of the production one.
func dirTreeWithBudget(url, sourceHash, relPath string, limit int) ([]*DirItem, error) {
	return newDirWalker(url, limit).tree(sourceHash, relPath)
}

// tree fetches the root blob and, when it is a directory, its children.
func (w *dirWalker) tree(hash, relPath string) ([]*DirItem, error) {
	blob, isDir, size, err := fetchBlob(w.url, hash)
	if err != nil {
		return nil, err
	}
	w.nodes++
	if w.nodes > w.limit {
		return nil, errManifestTooLarge
	}

	item := &DirItem{
		Hash: hash,
		Name: relPath,
		Path: relPath,
		Size: size,
	}
	if !isDir {
		item.URL = w.url + "/" + hash
		item.MediaType = "file"
		return []*DirItem{item}, nil
	}

	item.MediaType = "dir"
	// Parse the already-fetched root manifest (do not re-fetch via children)
	// and seed the cache so a subtree that names the root again is free.
	kids, err := w.parse(blob, relPath, 0)
	if err != nil {
		return nil, err
	}
	w.cache[hash] = cachedDir{base: relPath, items: kids}
	item.Children = kids
	return []*DirItem{item}, nil
}

// children returns the parsed contents of the directory at hash, fetching and
// parsing its manifest at most once per walk. A cache hit under a different
// base clones the subtree (repath) rather than aliasing Path values, so the
// same content addressed at two locations stays selectable at both.
func (w *dirWalker) children(hash, base string, depth int) ([]*DirItem, error) {
	if depth > maxDirDepth {
		return nil, fmt.Errorf("directory nesting exceeds %d levels (possible malicious manifest)", maxDirDepth)
	}
	if c, ok := w.cache[hash]; ok {
		if c.base == base {
			return c.items, nil
		}
		return w.repath(c.items, base)
	}
	blob, _, _, err := fetchBlob(w.url, hash)
	if err != nil {
		return nil, err
	}
	items, err := w.parse(blob, base, depth)
	if err != nil {
		return nil, err
	}
	w.cache[hash] = cachedDir{base: base, items: items}
	return items, nil
}

// parse turns one directory manifest into items, recursing only into entries
// whose inline head marks them as directories.
func (w *dirWalker) parse(manifest []byte, base string, depth int) ([]*DirItem, error) {
	var items []*DirItem
	scanner := bufio.NewScanner(bytes.NewReader(manifest))
	for scanner.Scan() {
		// Malformed lines and the two header lines fail to parse and are skipped.
		entry, ok := metadata.ParseDirLine(scanner.Text())
		if !ok {
			continue
		}
		w.nodes++
		if w.nodes > w.limit {
			return nil, errManifestTooLarge
		}
		child := &DirItem{
			Name: entry.Filename,
			Path: path.Join(base, entry.Filename),
			Hash: entry.Hash,
			Size: entry.Size,
			Head: entry.Head,
		}
		if metadata.IsDirHead(entry.Head) {
			child.MediaType = "dir"
			kids, err := w.children(entry.Hash, child.Path, depth+1)
			if err != nil {
				return nil, err
			}
			child.Children = kids
		} else {
			child.MediaType = "file"
			child.URL = w.url + "/" + entry.Hash
		}
		items = append(items, child)
	}
	return items, scanner.Err()
}

// repath clones a cached subtree under a new base so a shared subtree keeps
// correct Path values at every occurrence. The clone counts against the node
// budget, bounding the cost of re-expanding wide shared subtrees.
func (w *dirWalker) repath(items []*DirItem, base string) ([]*DirItem, error) {
	out := make([]*DirItem, 0, len(items))
	for _, it := range items {
		w.nodes++
		if w.nodes > w.limit {
			return nil, errManifestTooLarge
		}
		c := *it
		c.Path = path.Join(base, it.Name)
		if len(it.Children) > 0 {
			kids, err := w.repath(it.Children, c.Path)
			if err != nil {
				return nil, err
			}
			c.Children = kids
		}
		out = append(out, &c)
	}
	return out, nil
}

// fetchBlob reports whether the blob at hash is a tie directory, returning its
// body only when it is. A file body is left unread because a job output can be
// many gigabytes and only manifests need their contents.
func fetchBlob(url, hash string) (manifest []byte, isDir bool, size int, err error) {
	resp, err := httpClient.Get(url + "/" + hash)
	if err != nil {
		return nil, false, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, 0, fmt.Errorf("bad status for %s: %s", hash, resp.Status)
	}
	if resp.ContentLength > 0 {
		size = int(resp.ContentLength)
	}

	header := make([]byte, len(metadata.DirHeader))
	n, err := io.ReadFull(resp.Body, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false, size, err
	}
	if string(header[:n]) != metadata.DirHeader {
		return nil, false, size, nil
	}

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, size, err
	}
	manifest = append(header[:n], rest...)
	return manifest, true, len(manifest), nil
}
