package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"git.sr.ht/~uid/tie/request"
	"git.sr.ht/~uid/tie/tiedb"
	"github.com/julienschmidt/httprouter"
)

var (
	RequestsToAnswer chan *Request
)

type Request struct {
	AnswerTo    http.ResponseWriter
	RawData     []byte
	RequestType request.Request
	Key         tiedb.CollectionKey
	Wait        sync.WaitGroup
}

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
			if len(set.Value2) == 0 {
				fmt.Fprintf(w, "Not found")
				return
			}

			if len(set.Value2) > 1 && selection == -1 {
				set, _ = col.SetToString(id, ".md", assPtr)
				renderer, err := GetSelectionRenderer(filename, set.Value2)
				if err != nil {
					fmt.Fprintf(w, "Internal error: "+err.Error())
				}
				renderer.template.Execute(w, renderer.data)
				return
			} else {
				selection = 0
			}

			filename = set.Value2[selection]
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
	raw_data, _ := io.ReadAll(r.Body)
	key := tiedb.CollectionKey{
		Database:   filepath.Join(dbPath, ps.ByName("database")),
		Collection: ps.ByName("collection"),
	}

	req := &Request{
		AnswerTo: w,
		RawData:  raw_data,
		Key:      key,
	}

	switch ps.ByName("type") {
	case "Add":
		log.Println("Request from " + r.RemoteAddr + ": Add")
		req.RequestType = &request.Add{}

	case "Get":
		log.Println("Request from " + r.RemoteAddr + ": Get")
		req.RequestType = &request.Get{}

	case "Delete":
		log.Println("Request from " + r.RemoteAddr + ": Delete")
		req.RequestType = &request.Delete{}

	case "Update":
		log.Println("Request from " + r.RemoteAddr + ": Update")
		req.RequestType = &request.Update{}

	case "Batch":
		log.Println("Request from " + r.RemoteAddr + ": Batch")
		req.RequestType = &request.Batch{}

	default:
		log.Println("Unrecognized request from " + r.RemoteAddr + ": " + ps.ByName("type"))
	}

	req.Wait.Add(1)
	RequestsToAnswer <- req
	req.Wait.Wait() // Wait until request is answered otherwise ResponseWriter will be closed
}

func StartRequestAnswerer() {
	go func() {
		for req := range RequestsToAnswer {
			AnswerRequest(req)
			req.Wait.Done()
		}
	}()
}

func ErrorToJsonString(prepend string, err error) string {
	msg := request.ReplyStatus{
		Success: false,
		Message: prepend + " " + err.Error(),
	}
	json_reply, err := json.Marshal(msg)

	if err != nil {
		return "[\"Success\": false, \"Message\": \"Internal error: " + err.Error() + "\"]"
	}

	return string(json_reply)
}

func AnswerRequest(req *Request) {
	errRequest := json.Unmarshal(req.RawData, req.RequestType)
	if errRequest != nil {
		log.Println(errRequest)
		msg := ErrorToJsonString("Error unmarshalling request:", errRequest)
		fmt.Fprint(req.AnswerTo, msg)
		return
	}

	reply, errReply := req.RequestType.Reply(db, req.Key)
	if errReply != nil {
		msg := ErrorToJsonString("Error:", errReply)
		fmt.Fprint(req.AnswerTo, msg)
	} else {
		json_reply, errMarshal := json.Marshal(reply.ReplyStruct)
		if errMarshal != nil {
			msg := ErrorToJsonString("Internal error:", errMarshal)
			fmt.Fprint(req.AnswerTo, msg)
		} else {
			fmt.Fprint(req.AnswerTo, string(json_reply))
		}
	}
}
