package request

import (
	"errors"
	"fmt"
	"strings"

	"git.sr.ht/~uid/tie/tiedb"
)

func (r *Batch) Reply(db *tiedb.Tree, key tiedb.CollectionKey) (Reply, error) {
	batch := ReplyBatch{}

	for _, x := range r.Update {
		reply, err := x.Reply(db, key)
		if err == nil {
			batch.Update = append(batch.Update, reply.ReplyStruct.(ReplyStatus))
		} else {
			return Reply{
				ReplyType:   ReplyTypeEmpty,
				ReplyStruct: nil,
			}, err
		}
	}

	for _, x := range r.Delete {
		reply, err := x.Reply(db, key)
		if err == nil {
			batch.Delete = append(batch.Delete, reply.ReplyStruct.(ReplyStatus))
		} else {
			return Reply{
				ReplyType:   ReplyTypeEmpty,
				ReplyStruct: nil,
			}, err
		}
	}

	for _, x := range r.Add {
		reply, err := x.Reply(db, key)
		if err == nil {
			batch.Add = append(batch.Add, reply.ReplyStruct.(ReplyStatus))
		} else {
			return Reply{
				ReplyType:   ReplyTypeEmpty,
				ReplyStruct: nil,
			}, err
		}
	}

	for _, x := range r.Get {
		reply, err := x.Reply(db, key)
		if err == nil {
			batch.Get = append(batch.Get, reply.ReplyStruct.([]*tiedb.StringSliceSet))
		} else {
			fmt.Println("Error:", err.Error())
			return Reply{
				ReplyType:   ReplyTypeEmpty,
				ReplyStruct: nil,
			}, err
		}
	}

	return Reply{
		ReplyType:   ReplyTypeBatch,
		ReplyStruct: batch,
	}, nil
}

func (r *Get) Reply(db *tiedb.Tree, key tiedb.CollectionKey) (Reply, error) {
	col := db.GetCollection(key)

	if len(r.Values) == 1 { // GET
		found, assPtr := col.GetAssociations(r.Values[0])

		if found {
			// replySlice, errString := RequestToStringSlice(col, r.Value, r.Relation, assPtr)
			var replySlice []*tiedb.StringSliceSet
			var nextLevelRelation, relation []string
			for _, x := range r.NextLevelValues {
				if len(x) >= 1 {
					if x[0] == '+' {
						nextLevelRelation = append(nextLevelRelation, strings.TrimPrefix(x, "+"))
					} else {
						relation = append(relation, x)
					}
				}
			}
			if len(relation) > 1 {
				return Reply{
					ReplyType:   ReplyTypeEmpty,
					ReplyStruct: nil,
				}, errors.New("Error: Max filters = 1, maybe you ment to use 'tie filters' or with + in front.")
			}

			set, trees := col.SetToString(r.Values[0], r.Filter, assPtr)
			replySlice = append(replySlice, set)
			if len(nextLevelRelation) > 0 {
				for i, x := range trees {
					for _, rel := range nextLevelRelation {
						set2, _ := col.SetToString(set.Value2[i], rel, x)
						replySlice = append(replySlice, set2)
					}
				}
			}
			return Reply{
				ReplyType:   ReplyTypeGet,
				ReplyStruct: replySlice,
			}, nil
		}
		return Reply{
			ReplyType:   ReplyTypeEmpty,
			ReplyStruct: nil,
		}, nil
	} else { // JOIN
		var prev *tiedb.Tree
		var replySlice []*tiedb.StringSliceSet
		var value string
		var nextLevelRelation []string

		first := true
		for _, x := range r.Values {
			if len(x) >= 1 && x[0] == '+' {
				nextLevelRelation = append(nextLevelRelation, strings.TrimPrefix(x, "+"))
				continue
			}
			if first {
				value = x
				var found bool
				found, prev = col.GetAssociations(x)
				if !found {
					return Reply{
						ReplyType:   ReplyTypeEmpty,
						ReplyStruct: nil,
					}, errors.New("Did not find " + x)
				}
				first = false
			} else {
				found, second := col.GetAssociations(x)
				if !found {
					return Reply{
						ReplyType:   ReplyTypeEmpty,
						ReplyStruct: nil,
					}, errors.New("Did not find " + x)
				}
				prev = prev.InnerJoin(second, tiedb.AssociationComparator)
			}
		}

		set, trees := col.SetToString(value, r.Filter, prev)
		replySlice = append(replySlice, set)
		if len(nextLevelRelation) > 0 {
			for i, x := range trees {
				for _, rel := range nextLevelRelation {
					set2, _ := col.SetToString(set.Value2[i], rel, x)
					replySlice = append(replySlice, set2)
				}
			}
		}

		return Reply{
			ReplyType:   ReplyTypeGet,
			ReplyStruct: replySlice,
		}, nil
		//her
	}
}

func (r *Delete) Reply(db *tiedb.Tree, key tiedb.CollectionKey) (Reply, error) {
	col := db.GetCollection(key)
	success, msg := col.Delete(r.Key, r.Value1, r.Value2)
	if success {
		success, msg = col.Delete(r.Value2, tiedb.ASSOCIATED, r.Key)
	}
	reply := ReplyStatus{
		Success:    success,
		Message:    msg,
		OrigKey:    r.Key,
		OrigValue1: r.Value1,
		OrigValue2: r.Value2,
	}

	return Reply{
		ReplyType:   ReplyTypeStatus,
		ReplyStruct: reply,
	}, nil
}

func (r *Update) Reply(db *tiedb.Tree, key tiedb.CollectionKey) (Reply, error) {
	if r == nil {
		return Reply{
			ReplyType: ReplyTypeStatus,
			ReplyStruct: ReplyStatus{
				Success: false,
				Message: "Received nil request",
			},
		}, errors.New("Received nil request")
	}
	col := db.GetCollection(key)
	var success bool
	var msg string
	if r.AddOnFailure {
		success, msg = col.UpdateAdd(r.Key, r.Value1, r.Value2, r.NewValue2)
	} else {
		success, msg = col.Update(r.Key, r.Value1, r.Value2, r.NewValue2)
	}
	reply := ReplyStatus{
		Success:    success,
		Message:    msg,
		OrigKey:    r.Key,
		OrigValue1: r.Value1,
		OrigValue2: r.Value2,
	}

	return Reply{
		ReplyType:   ReplyTypeStatus,
		ReplyStruct: reply,
	}, nil
}

func (r *Add) Reply(db *tiedb.Tree, key tiedb.CollectionKey) (Reply, error) {
	reply := ReplyStatus{}

	a := db.GetCollection(key).Add(r.Key, r.Value1, r.Value2)

	if a != nil {
		reply.Success = true
		db.GetCollection(key).Add(r.Value2, tiedb.ASSOCIATED, r.Key)
	} else {
		reply.Success = false
		fmt.Println("ERROR", a)
		reply.Message = "Something went wrong"
	}
	reply.OrigKey = r.Key
	reply.OrigValue1 = r.Value1
	reply.OrigValue2 = r.Value2

	return Reply{
		ReplyType:   ReplyTypeStatus,
		ReplyStruct: reply,
	}, nil
}
