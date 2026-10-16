package tiedb

import "fmt"

// Op kinds accepted by Apply.
const (
	OpAdd         = "add"           // add (Key, Relation, Values[0])
	OpDelete      = "delete"        // delete (Key, Relation, Values[0]); absent is a no-op
	OpSet         = "set"           // make (Key, Relation) hold exactly Values
	OpUpdate      = "update"        // change Values[0] into NewValue
	OpUpdateAdd   = "update-add"    // like update, but adds NewValue when the source is absent
	OpAddIfAbsent = "add-if-absent" // add Values only if (Key, Relation) is empty
)

// Op is one mutation inside an Apply batch. Values carries the operand(s):
// one for add/delete/update, the full replacement set for set.
type Op struct {
	Kind     string
	Key      string
	Relation string
	Values   []string
	NewValue string
}

func (op Op) first() string {
	if len(op.Values) == 0 {
		return ""
	}
	return op.Values[0]
}

// Apply runs a list of ops as ONE critical section: it takes changeMutex once,
// validates every op, and only then applies them in order. A batch therefore
// never leaves a half-applied result — the failure mode the per-op write
// requests had — and no reader can observe an intermediate state of the batch
// through another writer's interleaving.
//
// Validation is strict for OpUpdate (the source triple must exist) and for an
// unknown kind; those fail the whole batch with applied=0 and nothing changed.
// OpDelete of an absent triple is a no-op, not a failure: callers routinely
// delete metadata that may not have been written.
//
// OpAddIfAbsent is the compare-and-set primitive: it writes Values only when
// (Key, Relation) currently holds nothing, which makes "create this path once"
// (MkTieDir) race-free without a separate round-trip.
//
// Add, Delete, Update and SetValues stay as single-op wrappers for callers
// that want one mutation and a message.
func (ic *Collection) Apply(ops []Op) (applied int, err error) {
	ic.changeMutex.Lock()
	defer ic.changeMutex.Unlock()

	for i, op := range ops {
		switch op.Kind {
		case OpAdd, OpDelete, OpSet, OpAddIfAbsent, OpUpdateAdd:
		case OpUpdate:
			if !ic.tripleExistsLocked(op.Key, op.Relation, op.first()) {
				return 0, fmt.Errorf("batch op %d (%s): (%s, %s, %s) not found",
					i, op.Kind, op.Key, op.Relation, op.first())
			}
		default:
			return 0, fmt.Errorf("batch op %d: unknown op %q", i, op.Kind)
		}
	}

	for i, op := range ops {
		switch op.Kind {
		case OpAdd:
			ic.addLocked(op.Key, op.Relation, op.first())
		case OpDelete:
			ic.deleteLocked(op.Key, op.Relation, op.first())
		case OpSet:
			ic.setValuesLocked(op.Key, op.Relation, op.Values)
		case OpUpdate:
			ic.deleteLocked(op.Key, op.Relation, op.first())
			ic.addLocked(op.Key, op.Relation, op.NewValue)
		case OpUpdateAdd:
			ic.deleteLocked(op.Key, op.Relation, op.first())
			ic.addLocked(op.Key, op.Relation, op.NewValue)
		case OpAddIfAbsent:
			if !ic.relationHasValuesLocked(op.Key, op.Relation) {
				for _, v := range op.Values {
					ic.addLocked(op.Key, op.Relation, v)
				}
			}
		default:
			return i, fmt.Errorf("batch op %d: unknown op %q", i, op.Kind)
		}
	}
	return len(ops), nil
}

// tripleExistsLocked reports whether the exact triple is in the index. The
// caller must hold changeMutex (reads take no lock of their own, so this is
// safe under it).
func (ic *Collection) tripleExistsLocked(key, relation, value string) bool {
	k, kLevel, found := ic.getEntryFromString(key)
	if !found {
		return false
	}
	v1, _, found := ic.getEntryFromString(relation)
	if !found {
		return false
	}
	v2, _, found := ic.getEntryFromString(value)
	if !found {
		return false
	}
	return ic.uniqueAssociationExists(kLevel, k, v1, v2)
}

// relationHasValuesLocked reports whether (key, relation) holds any value.
func (ic *Collection) relationHasValuesLocked(key, relation string) bool {
	v1, _, found := ic.getEntryFromString(relation)
	if !found {
		return false
	}
	set, found := ic.GetAssociations(key)
	if !found {
		return false
	}
	return set.HasRelation(v1)
}
