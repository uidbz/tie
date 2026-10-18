package client

// relink.go rebuilds the containment a bulk re-home flattened (#120).
//
// Every live directory node links the immutable tiedir manifest it was imported
// from (its "tiedir-hash" edge), and that manifest records each entry's content
// hash, name and size. So when an earlier repair scattered a folder's contents
// into one flat recovery directory, the record of what the folder held is still
// there, and still addresses its members by their real identity: a file by its
// content hash, a subdirectory by the manifest that *it* is the snapshot of.
// RelinkFromManifests reads those manifests and re-asserts the recorded
// (child, parent, directory) edges.
//
// Two guards keep it from duplicating healthy state:
//
//   - a child is relinked only when every parent it has today is a recovery
//     directory (RecoveryPrefix), or it has none at all. Anything sitting under
//     a live directory was put there deliberately — by a move, or because it was
//     never orphaned — and stays as it is;
//   - a subdirectory entry is resolved through the tiedir-hash link, never by
//     name, so the edge goes to the node that *is* that folder.
//
// Like the rest of repair, it plans before it mutates and journals what it
// applies; DropStale also removes the recovery-directory edges once the recorded
// one is in place, so a node ends up in one place rather than two.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/metadata"
)

// relinkWorkers is how many children are inspected at once. Each costs one read
// of its triples, and a collection flattened by a re-home has tens of thousands
// of them; the filehost and triplestore both answer far faster than one caller
// can queue up.
const relinkWorkers = 12

// RelinkOptions configures RelinkFromManifests.
type RelinkOptions struct {
	// Apply performs the mutations; when false the operations are only planned.
	Apply bool
	// RecoveryPrefix marks the directories a past re-home flattened; the default
	// is tie:/restored/, where RepairOrphans puts what it cannot place. Only
	// children whose every current parent is under it — and children with no
	// parent at all — are relinked. An empty value relaxes the rule to every
	// recorded child that is not already a child of the directory listing it.
	RecoveryPrefix string
	// DropStale deletes the recovery-directory parent edges once the recorded
	// one is in place.
	DropStale bool
	// ManifestLimit caps how many directory manifests are read (0 = all), to run
	// a pass over a bounded slice of a large collection first.
	ManifestLimit int
	// Journal receives one TSV line per applied operation. Ignored unless Apply
	// is set. May be nil.
	Journal io.Writer
	// Progress receives a line per manifest read that could not be used, plus a
	// summary. May be nil.
	Progress io.Writer
}

// relinkClaim is one manifest's statement that a child belongs to a directory.
type relinkClaim struct {
	dir  string // the DirUID whose manifest records it
	name string // the name as recorded, for the note
}

// RelinkFromManifests re-asserts, from every directory's tiedir manifest, the
// parent edges it records for children that today hang only off a recovery
// directory. The returned plan is in a stable order, so a plan a human reviewed
// is the plan that gets applied.
func (tie *TieClient) RelinkFromManifests(opts RelinkOptions) (*RepairResult, error) {
	if opts.RecoveryPrefix == "" {
		opts.RecoveryPrefix = FileURIScheme + "/restored/"
	}
	r := &repairSession{
		tie: tie,
		opts: RepairOptions{
			Apply: opts.Apply, Journal: opts.Journal, Progress: opts.Progress,
		},
		res:       &RepairResult{},
		liveDir:   map[string]bool{},
		ghosts:    map[string]bool{},
		done:      map[string]bool{},
		protected: map[string]bool{},
		ancestor:  map[string]string{},
		handled:   map[string]bool{},
		created:   map[string]string{},
	}

	dirRows, err := tie.allOfType(str(TieDirectory))
	if err != nil {
		return nil, err
	}
	recovery := map[string]bool{}
	type manifestDir struct{ uid, hash string }
	var withManifest []manifestDir
	for _, row := range dirRows {
		ps := RowValues(row, RelPath)
		if len(ps) == 0 {
			continue // no path: not a place anything can be relinked into
		}
		r.liveDir[row.Key] = true
		for _, p := range ps {
			if strings.HasPrefix(p, opts.RecoveryPrefix) {
				recovery[row.Key] = true
			}
		}
		if h := RowFirst(row, RelTiedirHash); h != "" {
			withManifest = append(withManifest, manifestDir{row.Key, h})
		}
	}
	// Stable order: the same collection yields the same plan twice.
	sort.Slice(withManifest, func(i, j int) bool { return withManifest[i].uid < withManifest[j].uid })
	if opts.ManifestLimit > 0 && len(withManifest) > opts.ManifestLimit {
		withManifest = withManifest[:opts.ManifestLimit]
	}

	// claims maps a child to the directories whose manifests list it. A file
	// entry names the child by its content address, which *is* its key; a
	// directory entry names the child's manifest, resolved below.
	claims := map[string][]relinkClaim{}
	var pendingDirs []struct{ entryHash, dir, name string }
	read, unusable := 0, 0
	for _, md := range withManifest {
		entries, err := r.readManifest(md.hash)
		if err != nil {
			unusable++
			r.progress("manifest %s of dir %s: %v", short(md.hash), short(md.uid), err)
			continue
		}
		read++
		for _, e := range entries {
			if metadata.IsDirHead(e.Head) {
				pendingDirs = append(pendingDirs, struct {
					entryHash, dir, name string
				}{e.Hash, md.uid, e.Filename})
				continue
			}
			claims[e.Hash] = append(claims[e.Hash], relinkClaim{md.uid, e.Filename})
		}
	}

	// Resolve subdirectory entries: the entry hash is the child's tiedir
	// snapshot, and the child node is whoever links it.
	for _, pd := range pendingDirs {
		rows, _, err := tie.Query(QuerySpec{
			Terms: []string{pd.entryHash}, Filter: string(RelTiedirHash), Reverse: true, Limit: -1,
		})
		if err != nil || len(rows) == 0 {
			r.progress("subdirectory %s (%s): no directory node links manifest %s",
				pd.name, short(pd.dir), short(pd.entryHash))
			continue
		}
		for _, row := range rows {
			if row.Key == pd.dir {
				continue // itself
			}
			claims[row.Key] = append(claims[row.Key], relinkClaim{pd.dir, pd.name})
		}
	}

	// Decide per child. Children are looked up once each, in parallel: on a
	// collection flattened by a re-home this is tens of thousands of reads.
	keys := make([]string, 0, len(claims))
	for k := range claims {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		ops    []RepairOp
		linked int
	)
	workers := relinkWorkers
	jobs := make(chan string)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				var parents []string
				row, err := tie.Get(key)
				if err != nil {
					if errors.Is(err, ErrNotFound) {
						// The child is gone from the store — deleted after the
						// import, or a manifest entry whose subject never made
						// it. Adding an edge would invent a node out of a single
						// triple, so a manifest is not allowed to resurrect it.
						continue
					}
					r.progress("%s: reading its triples: %v", short(key), err)
					continue
				}
				parents = RowValues(row, RelParent)
				var mine []relinkClaim
				for _, c := range claims[key] {
					if !slices.Contains(parents, c.dir) {
						mine = append(mine, c)
					}
				}
				if len(mine) == 0 {
					continue // every claim is already an edge
				}
				// Only a child whose whole present home is recovery dirs (or
				// nothing) is a re-home casualty; anything else was placed where
				// it is on purpose.
				orphanedElsewhere := false
				for _, p := range parents {
					if !recovery[p] {
						orphanedElsewhere = true
						break
					}
				}
				if orphanedElsewhere {
					continue
				}
				local := make([]RepairOp, 0, len(mine)+len(parents))
				for _, c := range mine {
					local = append(local, RepairOp{api.BatchAdd, key, string(RelParent), c.dir,
						fmt.Sprintf("recorded by %s's manifest as %s", short(c.dir), c.name)})
				}
				if opts.DropStale {
					for _, p := range parents {
						if recovery[p] {
							local = append(local, RepairOp{api.BatchDelete, key, string(RelParent), p,
								"recovery home replaced by the recorded one"})
						}
					}
				}
				mu.Lock()
				ops = append(ops, local...)
				linked++
				mu.Unlock()
			}
		}()
	}
	for _, k := range keys {
		jobs <- k
	}
	close(jobs)
	wg.Wait()

	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Key != ops[j].Key {
			return ops[i].Key < ops[j].Key
		}
		if ops[i].Kind != ops[j].Kind {
			return ops[i].Kind < ops[j].Kind
		}
		return ops[i].Value < ops[j].Value
	})
	r.progress("manifests read: %d (unusable: %d), children relinked: %d", read, unusable, linked)
	if err := r.run("relink from manifests", ops); err != nil {
		return r.res, err
	}
	return r.res, nil
}

// readManifest fetches one tiedir snapshot blob and parses its entries. The
// bytes are hashed on the way in: a manifest is addressed by its own hash, so a
// mismatch means the fetch was not what the collection claims.
func (r *repairSession) readManifest(hash string) ([]metadata.DirEntry, error) {
	fh, err := r.blobFH()
	if err != nil {
		return nil, err
	}
	body, _, err := fh.GetContent(hash)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	got, err := metadata.HashReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if got != hash {
		return nil, fmt.Errorf("bytes hash to %s, not %s", short(got), short(hash))
	}
	if !bytes.HasPrefix(data, []byte("tiedir-v")) {
		return nil, errors.New("not a tiedir manifest")
	}
	var out []metadata.DirEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "tiedir-v") ||
			strings.HasPrefix(line, "---") {
			continue
		}
		e, ok := metadata.ParseDirLine(line)
		if !ok {
			r.progress("%s: skipping a malformed manifest line", short(hash))
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
