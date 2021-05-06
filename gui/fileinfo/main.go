package main

import (
	"encoding/json"
	"fmt"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"git.sr.ht/~uid/putlib"
	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/gui/component"
	"git.sr.ht/~uid/tie/request"
)

var (
	view *component.TieView
)

func GetHandler(resp json.RawMessage) {
	var result []request.ReplyGet
	err := json.Unmarshal(resp, &result)
	if err != nil {
		fmt.Println("Error handling Get reponse:", err)
	}

	// fmt.Println(result)
	if len(result) > 0 {
		view.SetData(result[0].Relations, result[0].Associations)
	}
	// db := tiedb.NewDB(false)
	// col := db.GetCollection(tiedb.CollectionKey{"tmp", "results"})
	// for _, x := range result {
	// 	for i, _ := range x.Associations {
	// 		key := x.Item
	// 		value1 := x.Relations[i]
	// 		value2 := x.Associations[i]
	// 		fmt.Println(key, value1, value2)
	// 		// col.Add(key, value1, value2)
	// 	}
	// }
}

func main() {
	var hash string
	if len(os.Args) > 1 {
		pc := putlib.PutConfig{}
		hash, _ = pc.AddressOfFile(os.Args[1])
	}
	pc := putlib.PutConfig{}
	hash, _ = pc.AddressOfFile("test")

	view = component.NewTieGUIComponent()
	view.SetKey(hash)
	view.SetData([]string{}, []string{})
	myApp := app.New()
	w := myApp.NewWindow("fileinfo")

	w.SetContent(view.MakeUI())

	w.Resize(fyne.NewSize(150, 100))

	tie.InitConfig()
	g := request.Get{
		Values: []string{hash},
	}
	b, _ := json.Marshal(g)
	tie.SendToWebservice("Get", b, GetHandler)
	w.ShowAndRun()
}
