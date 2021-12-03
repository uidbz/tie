// tie-img project main.go
package main

import (
	"flag"
	"fmt"
	"os"

	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"

	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/gui/component"
	"git.sr.ht/~uid/tie/io/putlib"
)

var (
	imgPath    string
	refreshImg func()
	tieView    *component.TieView
)

func GetFileInfo(path string) {
	pc := putlib.PutConfig{}
	hash, _ := pc.AddressOfFile(path)
	fmt.Println(hash)
	tieView.CurrentFilePath = path

	tieView.SetKey(hash)
	tieView.SetData([]string{}, []string{})
}

func main() {
	set := flag.NewFlagSet("", flag.ExitOnError)
	var config string
	set.StringVar(&config, "config", "config", "Set current tie config")

	if len(os.Args) > 1 {
		imgPath = os.Args[1]
		if err := set.Parse(os.Args[2:]); err != nil {
			fmt.Println(err)
		}
	} else {
		fmt.Println("Need path to image as first argument")
		return
	}

	ReadDir(imgPath)

	myApp := app.New()
	w := myApp.NewWindow("Image")

	fmt.Println("Current config:", config)
	tie.Config = config
	tie.InitConfig()
	tie.CurrentState.Verbose = true

	tieView = component.NewTieView()
	GetFileInfo(imgPath)

	c := container.NewMax()
	tieView.MakeUI(c)
	image := NewImgView(imgPath, w.Canvas().Focus)
	split := container.NewHSplit(image, c)
	split.Offset = 0.8
	refreshImg = split.Refresh
	w.SetContent(split)
	w.Canvas().Focus(image)
	w.ShowAndRun()
}
