package component

import (
	"fyne.io/fyne/v2"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"git.sr.ht/~uid/tie/client"
)

type TieCreateView struct {
	Object         fyne.CanvasObject
	SelectedKey    binding.String
	SelectedValue1 binding.String
	SelectedValue2 binding.String

	parent *TieView
}

func NewTieCreateView() *TieCreateView {
	tv := TieCreateView{}
	tv.SelectedKey = binding.NewString()
	tv.SelectedValue1 = binding.NewString()
	tv.SelectedValue2 = binding.NewString()

	return &tv
}

func (tv *TieCreateView) SetKey(key string) {
	tv.SelectedKey.Set(key)
}

func (tv *TieCreateView) Add() {
	key, _ := tv.SelectedKey.Get()
	val1, _ := tv.SelectedValue1.Get()
	val2, _ := tv.SelectedValue2.Get()
	tie.TieAdd(key, val1, val2, tv.parent.AddHandler)
	tv.parent.window.SetContent(tv.parent.MakeUI(tv.parent.window))

}

func (tv *TieCreateView) Exit() {
	tv.parent.window.SetContent(tv.parent.MakeUI(tv.parent.window))
}

func (tv *TieCreateView) MakeUI(parent *TieView) fyne.CanvasObject {
	tv.parent = parent

	exit := widget.NewButton("Close", tv.Exit)
	add := widget.NewButton("Add tags", tv.Add)

	txtKey := widget.NewEntryWithData(tv.SelectedKey)
	txtValue1 := widget.NewEntryWithData(tv.SelectedValue1)
	txtValue2 := widget.NewEntryWithData(tv.SelectedValue2)
	middle := container.NewVBox(txtKey, txtValue1, txtValue2)
	bottom := container.NewGridWithColumns(2, exit, add)

	return container.NewBorder(widget.NewLabel("Create tag"), bottom, nil, nil, middle)
}

// func AddHandler(resp json.RawMessage) {
// 	s := request.ReplyStatus{}
// 	if err := json.Unmarshal(resp, &s); err == nil {
// 		if tie.CurrentState.Verbose || !s.Success {
// 			fmt.Println(resp)
// 		}
// 	} else {
// 		fmt.Println("Error unmarshalling response:", err, resp)
// 	}

// }
