package main

import (
	"fmt"

	"git.sr.ht/~uid/tie/request"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/gui/component"
	"git.sr.ht/~uid/tie/io/putlib"
	"github.com/spf13/cobra"
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
	get := request.NewGetRequest([]string{"music"})
	reply, err := tie.Run(get)
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

	view := component.NewTieView()
	addView := component.NewTieAddView()

	var rootCmd = &cobra.Command{Use: "tie-tag"}
	rootCmd.PersistentFlags().StringVarP(&tie.Config, "config", "c", "config", "Config file to load")
	rootCmd.PersistentFlags().StringVarP(&view.CurrentFilePath, "input", "i", "", "File to tag")
	rootCmd.PersistentFlags().BoolVarP(&addView.ImportAddClose, "add", "a", false, "Go directly to 'add' view")
	rootCmd.PersistentFlags().BoolVarP(&tie.CurrentState.Verbose, "verbose", "v", true, "Verbose output")

	rootCmd.Execute()

	tie.InitConfig()

	pc := putlib.PutConfig{}

	if view.CurrentFilePath != "" {
		if addView.ImportAddClose {
			go func() {
				addView.HashCalculated = make(chan bool, 1)
				hash, _ = pc.AddressOfFile(view.CurrentFilePath)
				view.SetKey(hash)
				addView.SetKey(hash)
				addView.HashCalculated <- true
			}()
		} else {
			hash, _ = pc.AddressOfFile(view.CurrentFilePath)
		}
	} else {
		hash, _ = pc.AddressOfFile("main.go")
		view.CurrentFilePath = "main.go"
	}

	view.SetKey(hash)
	view.SetData([]string{}, []string{})
	myApp := app.New()
	w := myApp.NewWindow("fileinfo")

	c := container.NewMax()
	view.MakeUI(c)
	if addView.ImportAddClose {
		addView.SetData([]string{}, []string{})
		addView.GetTags()
		view.UpdateUI(addView.MakeUI(view))
	}
	// var tt TieTag
	// tt.data = binding.BindStringList(&[]string{"hej"})
	// tt.Init()
	// w.SetContent(tt.MakeUI(tieview))
	w.SetContent(c)

	w.Resize(fyne.NewSize(1000, 1000))
	view.Refresh()

	w.ShowAndRun()
}
