package tiedb

// Forward/reverse index consistency check.
//
// The forward index (subject -> {relation, value2} -> position) is the source
// of truth: every on-disk association record is inserted into it at load, and
// the reverse index (value2 -> {subject, relation} -> position) is derived from
// it for the relations in ReverseRelations. The two are updated one after the
// other under no common lock, so a lost update in either tree leaves a triple
// visible through one index only: a subject with forward triples that a reverse
// query never returns, or a "phantom" that reverse queries return but whose
// forward set is empty and that Delete cannot clear. CheckIndex walks both
// trees and reports (and optionally repairs, in memory) every such divergence.

// IndexCheckOptions configures CheckIndex.
type IndexCheckOptions struct {
	// Deep additionally resolves every forward position and compares the stored
	// record with the index key that points at it. In disk mode this is one
	// serialized read per triple through the writer goroutine, so it is slow on
	// a large collection; the default cross-compare is purely in memory.
	Deep bool
	// Repair fixes what the check finds, in memory only: reverse entries missing
	// for a forward triple are added, reverse-only entries are dropped, reverse
	// positions are realigned to the forward position, and (Deep) a forward
	// entry whose record does not match is rewritten to a fresh slot. The
	// on-disk file is derived from forward records, so nothing else changes.
	Repair bool
	// SampleLimit caps the problem samples retained in the report (0 = 50).
	SampleLimit int
}

// Problem kinds reported by CheckIndex.
const (
	IndexMissingReverse   = "missing-reverse"   // forward triple with no reverse entry
	IndexReverseOnly      = "reverse-only"      // reverse entry with no forward triple (phantom)
	IndexPositionMismatch = "position-mismatch" // both present, different positions
	IndexMisresolved      = "misresolved"       // forward position holds a different record (Deep)
)

// IndexProblem is one sampled divergence.
type IndexProblem struct {
	Kind     string `json:"kind"`
	Key      string `json:"key"`
	Value1   string `json:"value1"`
	Value2   string `json:"value2"`
	Position int64  `json:"position"`
}

// IndexReport is the result of CheckIndex.
type IndexReport struct {
	ForwardEntries   int            `json:"forwardEntries"`
	ReverseEntries   int            `json:"reverseEntries"`
	MissingReverse   int            `json:"missingReverse"`
	ReverseOnly      int            `json:"reverseOnly"`
	PositionMismatch int            `json:"positionMismatch"`
	Misresolved      int            `json:"misresolved"`
	Repaired         int            `json:"repaired"`
	Deep             bool           `json:"deep"`
	Samples          []IndexProblem `json:"samples,omitempty"`
}

// Problems is the number of divergences found (before any repair).
func (r IndexReport) Problems() int {
	return r.MissingReverse + r.ReverseOnly + r.PositionMismatch + r.Misresolved
}

// indexFix is one deferred repair. Fixes are collected during the walks and
// applied afterwards, because the walks hold read locks on the very sets a fix
// would mutate.
type indexFix struct {
	kind    string
	level   int // subject level
	subject uint64
	v1      uint64
	v1Level int
	v2      uint64
	v2Level int
	pos     int64 // forward position (the authoritative one)
	revKey  UniqueAssociation
	revSet  *AssociationSet // reverse set holding a reverse-only entry
}

// levelOf finds the trie level (or HASH_LEVEL) an entry id lives at. Entry ids
// are allocated from one counter across every level, so the id is unique and
// the scan is at most levelCount+1 point lookups.
func (ic *Collection) levelOf(id uint64) (int, bool) {
	if ic.hashEntries != nil {
		if _, ok := ic.hashEntries.Get(id); ok {
			return HASH_LEVEL, true
		}
	}
	ic.secureLevel.RLock()
	n := ic.levelCount
	ic.secureLevel.RUnlock()
	for level := 0; level < n; level++ {
		lvl, ok := ic.levelAt(level)
		if !ok {
			continue
		}
		if _, found := lvl.entries.Get(id); found {
			return level, true
		}
	}
	return 0, false
}

// forwardTrees lists (level, tree) for every forward association tree.
func (ic *Collection) forwardTrees() []levelTree {
	return ic.assocTrees(func(l entryLevel) *lockedTree[uint64, *AssociationSet] { return l.associations }, ic.hashAssociations)
}

// reverseTrees lists (level, tree) for every reverse association tree.
func (ic *Collection) reverseTrees() []levelTree {
	return ic.assocTrees(func(l entryLevel) *lockedTree[uint64, *AssociationSet] { return l.reverseAssociations }, ic.hashReverseAssociations)
}

type levelTree struct {
	level int
	tree  *lockedTree[uint64, *AssociationSet]
}

func (ic *Collection) assocTrees(pick func(entryLevel) *lockedTree[uint64, *AssociationSet], hash *lockedTree[uint64, *AssociationSet]) []levelTree {
	ic.secureLevel.RLock()
	n := ic.levelCount
	ic.secureLevel.RUnlock()
	out := make([]levelTree, 0, n+1)
	for level := 0; level < n; level++ {
		if lvl, ok := ic.levelAt(level); ok {
			out = append(out, levelTree{level, pick(lvl)})
		}
	}
	if hash != nil {
		out = append(out, levelTree{HASH_LEVEL, hash})
	}
	return out
}

// CheckIndex cross-checks the forward and reverse association indexes and
// reports every divergence; with opts.Repair it also fixes them in memory.
//
// Writes are blocked for the duration (changeMutex): the check must see a
// stable pair of trees to avoid reporting an in-flight Add as a divergence, and
// a repair must not race a concurrent mutation of the same sets. Reads are
// unaffected. The in-memory pass is O(triples) map iterations; Deep adds one
// record read per forward triple.
func (ic *Collection) CheckIndex(opts IndexCheckOptions) IndexReport {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()

	limit := opts.SampleLimit
	if limit <= 0 {
		limit = 50
	}
	rep := IndexReport{Deep: opts.Deep}
	var fixes []indexFix

	sample := func(kind string, level int, subject uint64, v1Level int, v1 uint64, v2Level int, v2 uint64, pos int64) {
		if len(rep.Samples) >= limit {
			return
		}
		rep.Samples = append(rep.Samples, IndexProblem{
			Kind:     kind,
			Key:      ic.getValueString(level, subject),
			Value1:   ic.getValueString(v1Level, v1),
			Value2:   ic.getValueString(v2Level, v2),
			Position: pos,
		})
	}

	// Forward pass: every forward triple that should be reverse-indexed must
	// have a reverse twin at the same position; with Deep, its record must
	// match the key.
	for _, ft := range ic.forwardTrees() {
		level := ft.level
		ft.tree.ForEach(func(subject uint64, set *AssociationSet) {
			set.ForEach(func(ua UniqueAssociation, pos int64) {
				rep.ForwardEntries++
				v1, v2 := ua.Relation, ua.AssociateTo
				v1Level, ok1 := ic.levelOf(v1)
				v2Level, ok2 := ic.levelOf(v2)
				if !ok1 || !ok2 {
					// Dangling entry ids: nothing to resolve against. Count as
					// misresolved; unrepairable without the record.
					rep.Misresolved++
					sample(IndexMisresolved, level, subject, v1Level, v1, v2Level, v2, pos)
					return
				}
				t := Triple{Level: level, Key: subject, Value1Level: v1Level, Value1: v1, Value2Level: v2Level, Value2: v2}

				if opts.Deep {
					got, ok := ic.resolveTriple(pos)
					if !ok || got.Level != level || got.Key != subject || got.Value1 != v1 || got.Value2 != v2 {
						rep.Misresolved++
						sample(IndexMisresolved, level, subject, v1Level, v1, v2Level, v2, pos)
						if opts.Repair {
							fixes = append(fixes, indexFix{kind: IndexMisresolved, level: level, subject: subject,
								v1: v1, v1Level: v1Level, v2: v2, v2Level: v2Level, pos: pos})
						}
						// A rewrite moves the position; the reverse check below
						// would then be against a stale pos. The rewrite fix
						// realigns reverse itself.
						return
					}
				}

				if !ic.shouldBuildReverse(&t) {
					return
				}
				revKey := UniqueAssociation{AssociateTo: subject, Relation: v1}
				rpos, found := ic.getReverseAssociations(v2Level, v2).Get(revKey)
				switch {
				case !found:
					rep.MissingReverse++
					sample(IndexMissingReverse, level, subject, v1Level, v1, v2Level, v2, pos)
					if opts.Repair {
						fixes = append(fixes, indexFix{kind: IndexMissingReverse, v2: v2, v2Level: v2Level, revKey: revKey, pos: pos})
					}
				case rpos != pos:
					rep.PositionMismatch++
					sample(IndexPositionMismatch, level, subject, v1Level, v1, v2Level, v2, pos)
					if opts.Repair {
						fixes = append(fixes, indexFix{kind: IndexPositionMismatch, v2: v2, v2Level: v2Level, revKey: revKey, pos: pos})
					}
				}
			})
		})
	}

	// Reverse pass: every reverse entry must have a forward twin. Position
	// mismatches were already counted above.
	for _, rt := range ic.reverseTrees() {
		v2Level := rt.level
		rt.tree.ForEach(func(v2 uint64, set *AssociationSet) {
			set.ForEach(func(ua UniqueAssociation, pos int64) {
				rep.ReverseEntries++
				subject, v1 := ua.AssociateTo, ua.Relation
				subjLevel, ok := ic.levelOf(subject)
				if ok {
					if _, found := ic.getAssociations(subjLevel, subject).Get(UniqueAssociation{AssociateTo: v2, Relation: v1}); found {
						return
					}
				}
				rep.ReverseOnly++
				v1Level, _ := ic.levelOf(v1)
				sample(IndexReverseOnly, subjLevel, subject, v1Level, v1, v2Level, v2, pos)
				if opts.Repair {
					fixes = append(fixes, indexFix{kind: IndexReverseOnly, revSet: set, revKey: ua})
				}
			})
		})
	}

	for _, f := range fixes {
		switch f.kind {
		case IndexMissingReverse, IndexPositionMismatch:
			rev, ok := ic.reverseAssociationsTree(f.v2Level)
			if !ok {
				continue
			}
			putAssoc(rev, f.v2, f.revKey, f.pos)
		case IndexReverseOnly:
			f.revSet.Delete(f.revKey)
		case IndexMisresolved:
			ic.rewriteAssociation(f)
		}
		rep.Repaired++
	}
	return rep
}

// rewriteAssociation persists a forward triple whose recorded position no
// longer holds it: the record is written to a fresh slot and both indexes are
// pointed at it. The old slot is left alone — it either holds a tombstone
// already or belongs to another live triple.
func (ic *Collection) rewriteAssociation(f indexFix) {
	t := Triple{Level: f.level, Key: f.subject, Value1Level: f.v1Level, Value1: f.v1, Value2Level: f.v2Level, Value2: f.v2}
	fwdKey := UniqueAssociation{AssociateTo: f.v2, Relation: f.v1}
	revKey := UniqueAssociation{AssociateTo: f.subject, Relation: f.v1}

	var pos int64
	if ic.writeToDisk {
		pos = ic.allocSlot()
		if ic.cache != nil {
			ic.cache.Evict(f.pos)
		}
	} else {
		pos = ic.arenaStore(&t)
	}
	if fwd, ok := ic.associationsTree(f.level); ok {
		putAssoc(fwd, f.subject, fwdKey, pos)
	}
	if ic.shouldBuildReverse(&t) {
		if rev, ok := ic.reverseAssociationsTree(f.v2Level); ok {
			putAssoc(rev, f.v2, revKey, pos)
		}
	}
	if ic.writeToDisk {
		ic.finishedAdding.Add(1)
		ic.dBWriteQueue <- FileMod{EntryType: TYPE_ASSOCIATION, Level: f.level, Mode: FILE_ADD, Position: pos, Triple: &t}
	}
}
