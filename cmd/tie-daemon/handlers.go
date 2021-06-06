package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"git.sr.ht/~uid/tie/request"
	"git.sr.ht/~uid/tie/tiedb"
	"github.com/julienschmidt/httprouter"
)

func Index(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	fmt.Fprint(w, "It works!\n")
}

func GetHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	fmt.Println("her")
	key := tiedb.CollectionKey{
		Database:   filepath.Join(dbPath, ps.ByName("database")),
		Collection: ps.ByName("collection"),
	}
	col := db.GetCollection(key)
	id := ps.ByName("id")
	var selection int
	selected := false
	if ps.ByName("selection") == "" {
		selection = -1
	} else {
		selection, _ = strconv.Atoi(ps.ByName("selection"))
		selected = true
	}
	id = strings.Replace(id, "&slash;", "/", -1)
	found, assPtr := col.GetAssociations(id)
	if found {
		switch ps.ByName("type") {
		case "md":
			log.Println("Request from " + r.RemoteAddr + ": md")

			var filename string
			set, _ := col.SetToString(id, ".md", assPtr)
			if len(set.Associations) == 0 {
				fmt.Fprintf(w, "Not found")
				return
			}

			if len(set.Associations) > 1 && selection == -1 {
				set, _ = col.SetToString(id, ".md", assPtr)
				renderer, err := GetSelectionRenderer(filename, set.Associations)
				if err != nil {
					fmt.Fprintf(w, "Internal error: "+err.Error())
				}
				renderer.template.Execute(w, renderer.data)
				return
			} else {
				selection = 0
			}

			filename = set.Associations[selection]
			set, _ = col.SetToString(id, "", assPtr)
			renderer, err := GetDocumentRenderer(filename, set, selected)
			if err != nil {
				fmt.Fprintf(w, "Internal error: "+err.Error())
			}
			renderer.template.Execute(w, renderer.data)

			// switch len(set.Associations)  {
			// 	case: 1

			// 	case: 0
			// 		fmt.Fprintf(w, "Not found")

			// 	default:
			// }
			// if len(set.Associations) > 0 {
			// } else {
			// 	fmt.Fprintf(w, "Not found")
			// 	return
			// }

			// set = col.SetToString(id, "", assPtr)
			// renderer, err := GetDocumentRenderer(filename, set)
			// if err != nil {
			// 	fmt.Fprintf(w, "Internal error: "+err.Error())
			// }
			// renderer.template.Execute(w, renderer.data)
		}
	} else {
		fmt.Fprintf(w, "Not found")
		return
	}
}

func RequestHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	raw_data, _ := ioutil.ReadAll(r.Body)
	key := tiedb.CollectionKey{
		Database:   filepath.Join(dbPath, ps.ByName("database")),
		Collection: ps.ByName("collection"),
	}

	switch ps.ByName("type") {
	case "Add":
		log.Println("Request from " + r.RemoteAddr + ": Add")
		AnswerRequest(w, raw_data, &request.Add{}, key)

	case "Get":
		log.Println("Request from " + r.RemoteAddr + ": Get")
		AnswerRequest(w, raw_data, &request.Get{}, key)

	case "Delete":
		log.Println("Request from " + r.RemoteAddr + ": Delete")
		AnswerRequest(w, raw_data, &request.Delete{}, key)

	case "Update":
		log.Println("Request from " + r.RemoteAddr + ": Update")
		AnswerRequest(w, raw_data, &request.Update{}, key)

	default:
		log.Println("Unrecognized request from " + r.RemoteAddr + ": " + ps.ByName("type"))
	}
}

func AnswerRequest(w http.ResponseWriter, raw_data []byte, r request.Request, key tiedb.CollectionKey) {
	err := json.Unmarshal(raw_data, r)
	if err != nil {
		log.Println(err)
		fmt.Fprint(w, "error: "+err.Error())
	} else {
		fmt.Fprint(w, r.Reply(db, key))
	}
}
