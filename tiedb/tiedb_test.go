package tiedb

import (
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

func TestDeleteAssociation(t *testing.T) {
	db := NewDB(false)
	col := db.GetCollection(CollectionKey{"Collections", "test3"})

	col.Add("superkey", "value1", "file")
	col.Add("superkey", "value2", "file")
	col.Delete("superkey", "value2", "file")
	col.Add("superkey", "value3", "file2")
	col.Add("superkey", "value4", "file2")

	found, asses := col.GetAssociations("superkey")

	if found {
		out, _ := col.SetToString("superkey", "", asses)
		if out.Value2[0] != "value3" {
			t.Errorf("Error input1, got: %s, want: %s.", out.Value2[0], "value3")
		}
		if out.Value2[1] != "value4" {
			t.Errorf("Error input2, got: %s, want: %s.", out.Value2[1], "value4")
		}
	} else {
		t.Error("Error GetAssociations, did not find", "a")
	}
}

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
