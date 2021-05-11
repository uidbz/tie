package request

import (
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~uid/tie/tiedb"
)

func (r *Get) Reply(db *tiedb.Tree, key tiedb.CollectionKey) string {
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
				return "Error: Max filters = 1, maybe you ment to use 'tie filters' or with + in front."
			}

			set, trees := col.SetToString(r.Values[0], r.Filter, assPtr)
			replySlice = append(replySlice, set)
			if len(nextLevelRelation) > 0 {
				for i, x := range trees {
					for _, rel := range nextLevelRelation {
						set2, _ := col.SetToString(set.Associations[i], rel, x)
						replySlice = append(replySlice, set2)
					}
				}
			}

			json_reply, err := json.Marshal(replySlice)
			if err != nil {
				return "[]"
			}

			return string(json_reply)
		}
		return "[]"
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
					return "[]"
				}
				first = false
			} else {
				found, second := col.GetAssociations(x)
				if !found {
					return "[]"
				}
				prev = prev.InnerJoin(second, tiedb.AssociationComparator)
			}
		}

		set, trees := col.SetToString(value, r.Filter, prev)
		replySlice = append(replySlice, set)
		if len(nextLevelRelation) > 0 {
			for i, x := range trees {
				for _, rel := range nextLevelRelation {
					set2, _ := col.SetToString(set.Associations[i], rel, x)
					replySlice = append(replySlice, set2)
				}
			}
		}

		json_reply, err := json.Marshal(replySlice)

		if err != nil {
			return "[]"
		}

		return string(json_reply)
	}
}

func (r *Delete) Reply(db *tiedb.Tree, key tiedb.CollectionKey) string {
	col := db.GetCollection(key)
	success, msg := col.Delete(r.Key, r.Value2, r.Value1)
	reply := ReplyStatus{
		Success: success,
		Message: msg,
	}

	json_reply, err := json.Marshal(reply)
	if err != nil {
		return "Error creating reply"
	}

	return string(json_reply)
}

func (r *Update) Reply(db *tiedb.Tree, key tiedb.CollectionKey) string {
	col := db.GetCollection(key)
	success, msg := col.Update(r.Key, r.Value1, r.Value2, r.NewValue2)
	reply := ReplyStatus{
		Success: success,
		Message: msg,
	}

	json_reply, err := json.Marshal(reply)
	if err != nil {
		return "Error creating reply"
	}

	return string(json_reply)
}

func (r *Add) Reply(db *tiedb.Tree, key tiedb.CollectionKey) string {
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

	json_reply, err := json.Marshal(reply)

	if err != nil {
		return "Error creating reply"
	}

	return string(json_reply)
}
