package tiesfs

// Tests for the path-tree operations the transports drive: mkdir, rename,
// delete. They run against a real triplestore and filehost (internal/testenv),
// so what they check is the stored state, not a mock's.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/internal/testenv"
)

// newPropTree starts a fresh collection with a live Tree over it.
func newPropTree(t *testing.T) (*Tree, *client.TieClient) {
	t.Helper()
	env := testenv.StartT(t)

	cfg := client.Config{
		TripleStoreURL:   env.TripleStoreURL,
		Namespace:        "proptesting",
		Collection:       "treeops",
		DefaultFileHosts: []string{"default"},
		FileHosts:        map[string]client.FileHost{"default": {URL: env.FileHostURL}},
	}
	tc := client.NewTieClient(cfg)
	if err := tc.DropCollection(); err != nil {
		t.Fatalf("dropping collection: %v", err)
	}
	tr := NewTree(tc, tc.Config.FileHosts["default"], 1, false)
	t.Cleanup(func() { tr.Close() })
	return tr, tc
}

// checkConsistency runs the collection's own consistency check and returns a
// printable description of every problem, or "" when the tree is clean.
func checkConsistency(t *testing.T, tc *client.TieClient) string {
	t.Helper()
	if err := tc.Sync(); err != nil {
		return "sync: " + err.Error()
	}
	rep, err := tc.Verify("", false)
	if err != nil {
		return "verify: " + err.Error()
	}
	var b strings.Builder
	report := func(what string, n int) {
		if n == 0 {
			return
		}
		fmt.Fprintf(&b, "  %s: %d\n", what, n)
	}
	report("orphan dirs", len(rep.OrphanDirs))
	report("orphan files", len(rep.OrphanFiles))
	report("dangling parent refs", len(rep.DanglingParentRefs))
	report("cycles", len(rep.Cycles))
	report("duplicate paths", len(rep.DuplicatePaths))
	report("missing metadata", len(rep.MissingMetadata))
	for p, uids := range rep.DuplicatePaths {
		fmt.Fprintf(&b, "    %q claimed by %v\n", p, uids)
	}
	for _, r := range rep.DanglingParentRefs {
		fmt.Fprintf(&b, "    child %v -> missing parent %v\n", r.Child, r.Parent)
	}
	for _, g := range rep.MissingMetadata {
		kind := "file"
		if g.IsDir {
			kind = "dir"
		}
		fmt.Fprintf(&b, "    %s %v missing %v\n", kind, g.Subject, g.Missing)
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String()
}

// TestRenameIntoSelfIsRefused: moving a directory under itself makes the
// subtree unreachable from the root — the cycle verify reports — and a mount or
// an SMB client can ask for it with one rename. POSIX answers EINVAL for the
// same case; the FUSE adapter maps the sentinel onto exactly that.
func TestRenameIntoSelfIsRefused(t *testing.T) {
	tr, tc := newPropTree(t)

	if _, err := tr.Mkdir("/", "lib"); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Mkdir("/lib", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Mkdir("/lib/a", "b"); err != nil {
		t.Fatal(err)
	}

	err := tr.Rename("/lib", "a", "/lib/a/b", "x", false)
	if !errors.Is(err, ErrRenameLoop) {
		t.Errorf("rename /lib/a into /lib/a/b = %v, want ErrRenameLoop", err)
	}
	// Nothing was damaged: the subtree is still where it was.
	for _, p := range []string{"/lib/a", "/lib/a/b"} {
		uid, err := tr.Tie.DirUIDFromPath(client.FileURIScheme + p)
		if err != nil || uid == "" {
			t.Errorf("%q no longer resolves after the refused rename: uid=%q err=%v", p, uid, err)
		}
	}
	if problems := checkConsistency(t, tc); problems != "" {
		t.Errorf("the refused rename left the tree inconsistent:\n%s", problems)
	}

	// A rename onto itself is a no-op, as POSIX defines it.
	if err := tr.Rename("/lib", "a", "/lib", "a", false); err != nil {
		t.Errorf("rename onto itself = %v, want success", err)
	}
	// A plain rename still works, and takes its subtree with it.
	if err := tr.Rename("/lib", "a", "/lib", "a2", false); err != nil {
		t.Fatalf("plain rename = %v", err)
	}
	uid, err := tr.Tie.DirUIDFromPath(client.FileURIScheme + "/lib/a2/b")
	if err != nil || uid == "" {
		t.Errorf("/lib/a2/b no longer resolves: uid=%q err=%v", uid, err)
	}
}
