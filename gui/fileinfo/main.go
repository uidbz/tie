package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"git.sr.ht/~uid/tie/gui/component"
)

func main() {
	view := component.NewTieGUIComponent()
	view.SetKey("a")
	view.SetData([]string{}, []string{})
	myApp := app.New()
	w := myApp.NewWindow("fileinfo")

	w.SetContent(view.MakeUI())

	w.Resize(fyne.NewSize(150, 100))
	w.ShowAndRun()
}
