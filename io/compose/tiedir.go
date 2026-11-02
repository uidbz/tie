package compose

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/uidbz/tie/io/fhclient"
	"github.com/uidbz/tie/io/putlib"
	"github.com/uidbz/tie/metadata"
)

// TreeBuilder composes a tie directory tree from blobs that are already on the
// filehost. Only the directory manifests it synthesizes are uploaded, so
// assembling an input set costs a few KB no matter how much data it references.
//
// This is what lets one job's output become the next job's input without the
// client touching file content: a tie directory is just a list of
// hash/name/size/head lines, so re-shaping an input set is a manifest edit.
type TreeBuilder struct {
	root *treeNode
}

// treeNode is either a leaf referencing an existing blob, or an interior
// directory holding children keyed by their own name.
type treeNode struct {
	entry    *metadata.DirEntry
	children map[string]*treeNode
}

func NewTreeBuilder() *TreeBuilder {
	return &TreeBuilder{root: &treeNode{}}
}

// Add places entry at path, a slash-separated location relative to the tree
// root, creating interior directories as needed. The name stored in the
// manifest comes from path, not from entry.Filename, because a tie entry holds
// a single path component while callers work with paths.
func (b *TreeBuilder) Add(path string, entry metadata.DirEntry) error {
	parts, err := splitTreePath(path)
	if err != nil {
		return err
	}
	n := b.root
	for i, name := range parts {
		if n.entry != nil {
			return fmt.Errorf("tie tree: %q is a file and cannot contain %q",
				strings.Join(parts[:i], "/"), name)
		}
		if n.children == nil {
			n.children = make(map[string]*treeNode)
		}
		child, ok := n.children[name]
		if !ok {
			child = &treeNode{}
			n.children[name] = child
		}
		n = child
	}
	if len(n.children) > 0 {
		return fmt.Errorf("tie tree: %q is a directory and cannot also be a file", path)
	}
	e := entry
	if n.entry != nil {
		// Mirrors selectEntries' collision rule: the same
		// content under one name is an idempotent duplicate, but differing
		// content under one name silently loses a file — the caller must find
		// out now, not in the job's output.
		if n.entry.Hash == e.Hash {
			return nil
		}
		return fmt.Errorf("tie tree: name collision: %q added twice with different hashes (%s vs %s)",
			path, n.entry.Hash, e.Hash)
	}
	n.entry = &e
	return nil
}

// Empty reports whether nothing has been added. An empty tree still uploads to
// a valid (empty) tie directory.
func (b *TreeBuilder) Empty() bool {
	return b.root.entry == nil && len(b.root.children) == 0
}

// UploadEmptyDir stores the empty tie directory and returns its hash, for jobs
// that take no input. The runner requires an InputHash that actually resolves on
// the filehost, so the manifest is uploaded rather than assumed to be present.
func UploadEmptyDir(filehost, store string) (string, error) {
	return NewTreeBuilder().Upload(filehost, store)
}

// Upload writes every synthesized directory manifest and returns the root hash,
// which is the value to use as a job's InputHash.
func (b *TreeBuilder) Upload(filehost, store string) (string, error) {
	entry, err := b.root.upload(filehost, putlib.PutConfig{Store: store})
	if err != nil {
		return "", err
	}
	return entry.Hash, nil
}

func (n *treeNode) upload(filehost string, cfg putlib.PutConfig) (metadata.DirEntry, error) {
	if n.entry != nil {
		return *n.entry, nil
	}

	// putlib enumerates directories with os.ReadDir, which sorts by name.
	// Sorting here keeps an identical tree's hash identical regardless of how
	// it was assembled, which is what makes resume and dedupe work.
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Strings(names)

	var manifest strings.Builder
	manifest.WriteString(metadata.DirHeader)
	for _, name := range names {
		entry, err := n.children[name].upload(filehost, cfg)
		if err != nil {
			return metadata.DirEntry{}, err
		}
		entry.Filename = name
		manifest.WriteString(entry.Line())
	}

	return uploadBlob(filehost, cfg, manifest.String())
}

// uploadBlob PUTs body to the filehost under its content address and returns an
// entry describing it. UploadMultipart reports only transport errors, so a
// rejected upload surfaces as the response body landing in Hash; comparing it
// against the address we computed is what catches an HTTP-level failure.
func uploadBlob(filehost string, cfg putlib.PutConfig, body string) (metadata.DirEntry, error) {
	hash, err := cfg.AddressOf(strings.NewReader(body))
	if err != nil {
		return metadata.DirEntry{}, errors.New("Upload error: " + err.Error())
	}
	fh := fhclient.New(strings.TrimSuffix(filehost, "/"), cfg.Client)
	item := cfg.UploadMultipart(fh, hash, strings.NewReader(body), len(body), "")
	if item.ErrorMsg != "" {
		return metadata.DirEntry{}, errors.New("Upload error: " + item.ErrorMsg)
	}
	if item.Hash != hash {
		return metadata.DirEntry{}, fmt.Errorf(
			"Upload error: filehost rejected manifest %s: %s",
			hash, strings.TrimSpace(item.Hash))
	}
	return metadata.DirEntry{Hash: hash, Size: len(body), Head: headOf(body)}, nil
}

// headOf returns the leading bytes tie stores per entry for recomputable
// media-type detection, matching putlib's Peek(metadata.MagicNumber).
func headOf(body string) []byte {
	if len(body) > metadata.MagicNumber {
		body = body[:metadata.MagicNumber]
	}
	return []byte(body)
}

func splitTreePath(path string) ([]string, error) {
	var parts []string
	for _, name := range strings.Split(filepath.ToSlash(path), "/") {
		switch name {
		case "", ".":
			continue
		case "..":
			return nil, fmt.Errorf("tie tree: path %q escapes the tree root", path)
		}
		parts = append(parts, name)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("tie tree: path %q names nothing", path)
	}
	return parts, nil
}
