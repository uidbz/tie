package tiefs

// Property tests for the tree operations the transports drive (§9): random
// sequences of mkdir / write / rename / delete against a real triplestore and
// filehost, after which the collection's own consistency check (Verify) must
// report nothing. These operations are multi-request read-modify-write
// sequences with no server-side atomicity, which is exactly where a lost
// parent edge or a duplicated path would hide.

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/uidbz/tie/client"
)

// treeModel is what the test believes the tree looks like, so it can pick
// legal operations (a rename needs a live source, a delete-dir an empty one).
type treeModel struct {
	dirs  map[string]bool // slash paths, "/lib" included
	files map[string]bool // slash paths
}

func newModel() *treeModel {
	return &treeModel{dirs: map[string]bool{"/lib": true}, files: map[string]bool{}}
}

func (m *treeModel) liveDirs() []string {
	out := make([]string, 0, len(m.dirs))
	for d := range m.dirs {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (m *treeModel) liveFiles() []string {
	out := make([]string, 0, len(m.files))
	for f := range m.files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// move updates the model for renaming old -> neu, carrying a directory's whole
// subtree with it.
func (m *treeModel) move(old, neu string) {
	if m.files[old] {
		delete(m.files, old)
		m.files[neu] = true
		return
	}
	for d := range m.dirs {
		if d == old {
			delete(m.dirs, d)
			m.dirs[neu] = true
			continue
		}
		if strings.HasPrefix(d, old+"/") {
			delete(m.dirs, d)
			m.dirs[neu+d[len(old):]] = true
		}
	}
	for f := range m.files {
		if strings.HasPrefix(f, old+"/") {
			delete(m.files, f)
			m.files[neu+f[len(old):]] = true
		}
	}
}

// TestTreeOpsKeepTheTreeConsistent drives random legal operations and checks
// the collection after every single one, so a failure names the operation that
// caused it.
func TestTreeOpsKeepTheTreeConsistent(t *testing.T) {
	// A few seeds: the value of a property test is the shapes it stumbles into,
	// and one sequence only covers one.
	for _, seed := range []int64{20261016, 7, 991, 424242} {
		seed := seed
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			runTreeOps(t, seed, 120)
		})
	}
}

func runTreeOps(t *testing.T, seed int64, rounds int) {
	tr, tc := newPropTree(t)

	rng := rand.New(rand.NewSource(seed))
	m := newModel()

	if _, err := tr.Mkdir("/", "lib"); err != nil {
		t.Fatalf("creating the root: %v", err)
	}

	var script []string
	// Counts keep the test honest: a run where every operation happened to
	// fail would prove nothing.
	var ok = map[string]int{}
	for round := 0; round < rounds; round++ {
		var desc string
		var err error

		switch rng.Intn(10) {
		case 0, 1:
			// mkdir a fresh name under a live directory
			parent := pick(rng, m.liveDirs())
			name := fmt.Sprintf("d%d", rng.Intn(5))
			if m.dirs[path.Join(parent, name)] {
				desc = fmt.Sprintf("mkdir %s/%s (already there)", parent, name)
				break
			}
			desc = fmt.Sprintf("mkdir %s/%s", parent, name)
			if _, err = tr.Mkdir(parent, name); err == nil {
				m.dirs[path.Join(parent, name)] = true
			}
		case 2, 3, 4:
			// write (or rewrite) a file: a rewrite exercises versioning
			parent := pick(rng, m.liveDirs())
			name := fmt.Sprintf("f%d.txt", rng.Intn(4))
			p := path.Join(parent, name)
			desc = fmt.Sprintf("write %s (#%d)", p, round)
			if err = writeStaged(tr, parent, name, fmt.Sprintf("content of %s, round %d", p, round)); err == nil {
				m.files[p] = true
			}
		case 5, 6, 7:
			// rename a live child somewhere else
			srcs := append(append([]string{}, m.liveFiles()...), m.liveDirs()...)
			if len(srcs) == 0 {
				desc = "rename (nothing to move)"
				break
			}
			src := pick(rng, srcs)
			dstDir := pick(rng, m.liveDirs())
			newName := fmt.Sprintf("r%d", rng.Intn(6))
			dst := path.Join(dstDir, newName)
			replace := m.files[dst] || m.dirs[dst]
			desc = fmt.Sprintf("rename %s -> %s (replace=%v)", src, dst, replace)
			if err = tr.Rename(path.Dir(src), path.Base(src), dstDir, newName, replace); err == nil {
				m.move(src, dst)
			}
		case 8:
			// delete a live file
			files := m.liveFiles()
			if len(files) == 0 {
				desc = "delete file (none)"
				break
			}
			f := pick(rng, files)
			desc = fmt.Sprintf("delete file %s", f)
			if err = tr.DeleteFile(path.Dir(f), path.Base(f)); err == nil {
				delete(m.files, f)
			}
		default:
			// delete an empty live directory (deepest first, so it is empty)
			dirs := m.liveDirs()
			sort.Slice(dirs, func(i, j int) bool { return strings.Count(dirs[i], "/") > strings.Count(dirs[j], "/") })
			var victim string
			for _, d := range dirs {
				if d == "/lib" || !dirHasChildren(m, d) {
					victim = d
					break
				}
			}
			if victim == "" {
				desc = "delete dir (none empty)"
				break
			}
			desc = fmt.Sprintf("delete dir %s", victim)
			if err = tr.DeleteDir(path.Dir(victim), path.Base(victim)); err == nil {
				delete(m.dirs, victim)
			}
		}

		script = append(script, desc)
		if err == nil {
			ok[strings.Fields(desc)[0]]++
		}
		if err != nil && !isExpectedFailure(err) {
			t.Fatalf("%s: %v\nscript:\n%s", desc, err, join(script))
		}
		if problems := checkConsistency(t, tc); problems != "" {
			t.Errorf("after %s the tree is inconsistent:\n%s\nscript:\n%s", desc, problems, join(script))
			break
		}
		if wrong := modelDisagrees(tr, m); wrong != "" {
			t.Errorf("after %s the store disagrees with what the operations did:\n%s\nscript:\n%s", desc, wrong, join(script))
			break
		}
	}

	t.Logf("succeeded: mkdir=%d write=%d rename=%d delete=%d (of %d rounds)",
		ok["mkdir"], ok["write"], ok["rename"], ok["delete"], rounds)
	for kind, want := range map[string]int{"mkdir": 5, "write": 15, "rename": 8, "delete": 3} {
		if ok[kind] < want {
			t.Errorf("only %d %s operations succeeded, want at least %d: the run proved little", ok[kind], kind, want)
		}
	}
	if t.Failed() {
		t.Logf("script:\n%s", join(script))
	}
}

// modelDisagrees checks the positive direction Verify cannot: Verify proves the
// graph is well formed, not that it still holds the children the operations put
// there. Every directory the model believes in must resolve, and every file
// must still be listed in its parent.
func modelDisagrees(tr *Tree, m *treeModel) string {
	var b strings.Builder
	for _, d := range m.liveDirs() {
		uid, err := tr.Tie.DirUIDFromPath(client.FileURIScheme + d)
		if err != nil {
			fmt.Fprintf(&b, "  %s: %v\n", d, err)
			continue
		}
		if uid == "" {
			fmt.Fprintf(&b, "  %s: no UID claims this directory\n", d)
		}
	}
	for _, f := range m.liveFiles() {
		dir, name := path.Dir(f), path.Base(f)
		pd, err := tr.PathDir(dir)
		if err != nil {
			fmt.Fprintf(&b, "  %s: parent %q unreadable: %v\n", f, dir, err)
			continue
		}
		found := false
		for _, x := range DisambiguateFiles(pd.Files) {
			if x.Filename == name {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(&b, "  %s: no longer listed in %q\n", f, dir)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String()
}

// isExpectedFailure accepts the errors that are a legal answer to a random
// operation (a collision, a missing source, a non-empty directory): the model
// and the store must agree about those, but they are not corruption.
func isExpectedFailure(err error) bool {
	return err != nil && (errors.Is(err, os.ErrExist) ||
		errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, ErrNotEmpty) ||
		errors.Is(err, ErrRenameLoop))
}

func dirHasChildren(m *treeModel, dir string) bool {
	for f := range m.files {
		if path.Dir(f) == dir {
			return true
		}
	}
	for d := range m.dirs {
		if d != dir && path.Dir(d) == dir {
			return true
		}
	}
	return false
}

// writeStaged writes a file through the mount's staged write path, which is
// what FUSE and SMB do: stage to a temp file, then commit (upload + triples).
func writeStaged(tr *Tree, parentSlash, name, content string) error {
	h, err := tr.NewWriteHandle(parentSlash, name, client.File{})
	if err != nil {
		return err
	}
	if _, err := h.WriteAt([]byte(content), 0); err != nil {
		h.Close()
		return err
	}
	return h.Close()
}

func pick(rng *rand.Rand, xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[rng.Intn(len(xs))]
}

func join(xs []string) string {
	var b strings.Builder
	for i, x := range xs {
		fmt.Fprintf(&b, "  %3d %s\n", i+1, x)
	}
	return b.String()
}
