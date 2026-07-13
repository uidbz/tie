package tiedb

import (
	"path/filepath"
	"testing"
)

//TODO: Rewrite tests

/*
func TestInsertAssociation(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"Collections", "test"})
	input1 := "/home/johan/gopath/src/nicecode.rocks/uid/tiedb"
	input2 := "/home/johan/gopath/src/nicecode.rocks/uid/tiedb2"

	col.Add("a", input1, "file")
	col.Add("a", input2, "file")

	found, asses := col.GetAssociations("a")

	if found {
		out, _ := col.SetToString("a", "", asses)
		if out.Value2[0] != input2 {
			t.Errorf("Error input1, got: %s, want: %s.", out.Value2[0], input2)
		}
		if out.Value2[1] != input1 {
			t.Errorf("Error input2, got: %s, want: %s.", out.Value2[1], input1)
		}
	} else {
		t.Error("Error GetAssociations, did not find", "a")
	}
}
// func TestInsertLongEntry(t *testing.T) {
// 	db := NewDB()
// 	col := db.GetCollection(CollectionKey{"Collections", "test2"})
// 	input1 := "/home/johan/gopath/src/nicecode.rocks/uid/tiedb"
// 	input2 := "/home/johan/gopath/src/nicecode.rocks/uid/tiedb2"

// 	e1 := col.Insert(input1)
// 	e2 := col.Insert(input2)

// 	ic := col.(*InternalCollection)

// 	val2 := ic.GetValueString(e2.Level, e2.Id)
// 	val1 := ic.GetValueString(e1.Level, e1.Id)

// 	if val1 != input1 {
// 		t.Errorf("Error input1, got: %s, want: %s.", val1, input1)
// 	}
// 	if val2 != input2 {
// 		t.Errorf("Error input2, got: %s, want: %s.", val2, input2)
// 	}
// }
*/

func TestAddAssociation(t *testing.T) {
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{"Collections", "test3"})

	col.Add("superkey", "value1", "value2-1")
	col.Add("superkey", "value1", "value2-2")
	// col.Delete("superkey", "value1-2", "value2-1")
	// col.Add("superkey", "value3", "file2")
	// col.Add("superkey", "value4", "file2")
	col.Sync()

	asses, found := col.GetAssociations("superkey")

	if found {
		out, _ := col.GetTripleSet(asses, "", SortOptions{Limit: -1})
		if !out["superkey"]["value1"].Has("value2-1") {
			t.Errorf("Error input1, didn't have %s", "value2-1")
		}
		if !out["superkey"]["value1"].Has("value2-2") {
			t.Errorf("Error input2, didn't have %s", "value2-1")
		}
	} else {
		t.Error("Error GetAssociations, did not find", "superkey")
	}
}

// reverseKeys returns the set of keys reachable via a reverse lookup on value2.
func reverseKeys(t *testing.T, col *Collection, value2 string) map[string]bool {
	t.Helper()
	keys := make(map[string]bool)
	rev, found := col.GetReverseAssociations(value2)
	if !found {
		return keys
	}
	set, _ := col.GetTripleSet(rev, "", SortOptions{Limit: -1})
	set.ForEachKey(func(k string) { keys[k] = true })
	return keys
}

// TestReverseRelationAllowlist verifies that only whitelisted relations get a
// reverse index while forward lookups still work for every relation.
func TestReverseRelationAllowlist(t *testing.T) {
	db := NewDB(true)
	db.SetDefaultReverseRelations([]string{"tag"})
	col := db.GetCollection(CollectionKey{t.TempDir(), "revfilter"})

	col.Add("hashA", "tag", "sometag")
	col.Add("hashB", "tag", "sometag")
	col.Add("hashA", "filename", "myfile.ext")
	col.Sync()

	// Reverse lookup on the whitelisted relation's value works.
	if got := reverseKeys(t, col, "sometag"); !got["hashA"] || !got["hashB"] {
		t.Errorf("reverse lookup on tag 'sometag' = %v, want hashA and hashB", got)
	}

	// Reverse lookup on a non-whitelisted relation's value yields nothing.
	if got := reverseKeys(t, col, "myfile.ext"); len(got) != 0 {
		t.Errorf("reverse lookup on filename 'myfile.ext' = %v, want empty", got)
	}

	// Forward lookup still works for the non-whitelisted relation.
	fwd, found := col.GetAssociations("hashA")
	if !found {
		t.Fatal("forward lookup on hashA failed")
	}
	out, _ := col.GetTripleSet(fwd, "filename", SortOptions{Limit: -1})
	if !out["hashA"]["filename"].Has("myfile.ext") {
		t.Errorf("forward filename lookup missing value, got %v", out)
	}
}

// TestReverseAllowlistSurvivesReload verifies the two-pass loader rebuilds the
// same reverse index after a close/reopen cycle.
func TestReverseAllowlistSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "revreload")

	db := NewDB(true)
	db.SetDefaultReverseRelations([]string{"tag"})
	col := db.GetCollection(CollectionKey{dbPath, "col"})
	col.Add("hashA", "tag", "sometag")
	col.Add("hashA", "filename", "myfile.ext")
	col.Sync()
	col.closeDB()

	db = NewDB(true)
	db.SetDefaultReverseRelations([]string{"tag"})
	col = db.GetCollection(CollectionKey{dbPath, "col"})

	if got := reverseKeys(t, col, "sometag"); !got["hashA"] {
		t.Errorf("after reload, reverse lookup on 'sometag' = %v, want hashA", got)
	}
	if got := reverseKeys(t, col, "myfile.ext"); len(got) != 0 {
		t.Errorf("after reload, reverse lookup on 'myfile.ext' = %v, want empty", got)
	}
}

/*

func TestReOpen(t *testing.T) {
	db := NewDB(true)
	col := db.GetCollection(CollectionKey{"Collections", "test4"})

	col.Add("superkey", "value1", "file0")
	col.Add("superkey", "value2", "file1")

	col.closeDB()
	db = NewDB(true)
	col = db.GetCollection(CollectionKey{"Collections", "test4"})

	col.Add("superkey2", "value3", "file2")
	col.Add("superkey2", "value4", "file3")

	found, asses := col.GetAssociations("superkey2")

	if found {
		out, _ := col.SetToString("superkey2", "", asses)
		if out.Value1[0] != "value3" {
			t.Errorf("Error input1, got: %s, want: %s.", out.Value1[0], "value3")
		}
		if out.Value1[1] != "value4" {
			t.Errorf("Error input2, got: %s, want: %s.", out.Value1[1], "value4")
		}
		if out.Value2[0] != "file2" {
			t.Errorf("Error input2, got: %s, want: %s.", out.Value2[0], "file2")
		}
		if out.Value2[1] != "file3" {
			t.Errorf("Error input2, got: %s, want: %s.", out.Value2[1], "file3")
		}
	} else {
		t.Error("Error GetAssociations, did not find", "superkey2")
	}

	found, asses = col.GetAssociations("superkey")

	if found {
		out, _ := col.SetToString("superkey", "", asses)
		if out.Value1[0] != "value1" {
			t.Errorf("Error input1, got: %s, want: %s.", out.Value1[0], "value1")
		}
		if out.Value1[1] != "value2" {
			t.Errorf("Error input2, got: %s, want: %s.", out.Value1[1], "value2")
		}
		if out.Value2[0] != "file0" {
			t.Errorf("Error input3, got: %s, want: %s.", out.Value2[0], "file0")
		}
		if out.Value2[1] != "file1" {
			t.Errorf("Error input4, got: %s, want: %s.", out.Value2[1], "file1")
		}
	} else {
		t.Error("Error GetAssociations, did not find", "superkey")
	}
}

func TestUpdate(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"Collections", "test5"})

	key := "superkey"
	val1 := "value1"
	val2 := "value2"
	val3 := "value3"
	col.Add(key, val1, val2)
	success, err := col.Update(key, val1, val2, val3)
	if !success {
		t.Errorf("Error Update returned false, msg: %s.", err)
	}

	found, asses := col.GetAssociations("superkey")

	if found {
		out, _ := col.SetToString(key, "", asses)
		if out.Value2[0] != val3 {
			t.Errorf("Error input1, got: %s, want: %s.", out.Value2[0], val3)
		}
	} else {
		t.Error("Error GetAssociations, did not find", "superkey")
	}
}
*/

//TODO: MAKE TEST that create newDB. Add. Query. Close DB. Read DB. Add more. Make same query. Tjeck if 2 queries are identical.
// I think we have error in reading DB.
