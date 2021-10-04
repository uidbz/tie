package main

import (
	"fmt"
	"os"

	"git.sr.ht/~uid/tie/request"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/gui/component"
	"git.sr.ht/~uid/tie/io/putlib"
)

type TieTag struct {
	data binding.ExternalStringList
}

func (tt *TieTag) MakeUI(view fyne.CanvasObject) fyne.CanvasObject {
	list := widget.NewListWithData(tt.data,
		func() fyne.CanvasObject {
			return widget.NewLabel("template")
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			o.(*widget.Label).Bind(i.(binding.String))
		})
	return container.NewHSplit(list, view)
}

func GetListData(key string) {

}

func (tt *TieTag) Init() {
	get := request.Get{
		Values: []string{"music"},
	}
	reply, err := tie.Run(request.RequestTypeGet, get)
	if err != nil {
		fmt.Println(err.Error())
	} else {
		for _, x := range *reply.DataGet() {
			for _, x := range x.Value2 {
				fmt.Println(x)
			}
		}
	}
}

func main() {
	var hash string

	tie.Config = "config"
	tie.InitConfig()
	tie.CurrentState.Verbose = true
	view := component.NewTieView()

	if len(os.Args) > 1 {
		pc := putlib.PutConfig{}
		hash, _ = pc.AddressOfFile(os.Args[1])
		view.CurrentFilePath = os.Args[1]
	} else {
		pc := putlib.PutConfig{}
		hash, _ = pc.AddressOfFile("main.go")
		view.CurrentFilePath = "main.go"
	}

	view.SetKey(hash)
	view.SetData([]string{}, []string{})
	myApp := app.New()
	w := myApp.NewWindow("fileinfo")

	tieview := view.MakeUI(w)
	// var tt TieTag
	// tt.data = binding.BindStringList(&[]string{"hej"})
	// tt.Init()
	// w.SetContent(tt.MakeUI(tieview))
	w.SetContent(tieview)

	w.Resize(fyne.NewSize(1000, 1000))

	w.ShowAndRun()
}
