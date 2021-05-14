package main

import (
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"git.sr.ht/~uid/putlib"
	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/gui/component"
)

func main() {
	var hash string

	tie.InitConfig()
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

	w.SetContent(view.MakeUI(w))

	w.Resize(fyne.NewSize(1000, 1000))

	w.ShowAndRun()
}
