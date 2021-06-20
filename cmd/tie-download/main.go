// get project main.go
package main

import (
	"fmt"
	"os"

	"git.sr.ht/~uid/tie/io/getlib"
)

// func DirContent(path string) ([]string, []string, []fyne.URI) {
// 	names := []string{}
// 	dirs := []string{}
// 	files := []fyne.URI{}
// 	dir, err := storage.ListerForURI(storage.NewFileURI(path))
// 	if err == nil {
// 		list, _ := dir.List()
// 		for _, x := range list {
// 			names = append(names, x.Name())
// 			files = append(files, x)
// 			s, errStat := os.Stat(x.Path())
// 			if errStat == nil {
// 				if s.IsDir() {
// 					dirs = append(dirs, x.Name())
// 				}
// 			}
// 		}
// 	}
// 	return names, dirs, files
// }

// func FileSeletion(win fyne.Window) *fyne.Container {
// 	selectedFileList = widget.NewList(GetLength, GetObj2, UpdateText)
// 	t := widget.NewTree(func(uid widget.TreeNodeID) []string {
// 		switch uid {
// 		case "":
// 			return fileList //[]string{"cars", "trains"}
// 		}
// 		// files, _, _ := DirContent(filepath.Join(path, uid))
// 		return fileList
// 	},
// 		func(uid widget.TreeNodeID) bool {
// 			if uid == "" {
// 				return true
// 			}
// 			for _, x := range directoryList {
// 				if uid == x {
// 					return true
// 				}
// 			}
// 			return false
// 		},
// 		func(_ bool) fyne.CanvasObject {
// 			b := widget.NewButton("Template", nil)
// 			return b
// 		},
// 		func(uid widget.TreeNodeID, _ bool, template fyne.CanvasObject) {
// 			button := template.(*widget.Button)
// 			button.SetText(uid)
// 			button.OnTapped = func() {
// 				selectedFiles = append(selectedFiles, uid)
// 				selectedFileList.Refresh()
// 			}
// 		})
// 	b := widget.NewButton("Open folder", func() {

// 		dialog.ShowFolderOpen(func(dir fyne.ListableURI, err error) {
// 			if err != nil { // there was an error - tell user
// 				dialog.ShowError(err, win)
// 				return
// 			}
// 			if dir == nil { // user cancelled
// 				return
// 			}
// 			fmt.Println("Listing dir", dir.Path())
// 			// d, _ := dir.List(
// 			// var path string
// 			// for _, item := range d {
// 			// 	log.Println("Item name", item.Name())
// 			// 	path = item.Path()
// 			// }
// 			fileList, directoryList, uriList = DirContent(dir.Path())
// 			t.Refresh()
// 		}, win)

// 	})
// 	c := container.NewGridWithColumns(2, t, selectedFileList)

// 	return container.NewBorder(b, nil, nil, nil, c)
// }
func main() {
	if len(os.Args) > 2 {
		source := os.Args[1]
		dest := os.Args[2]
		var url string
		if len(os.Args) >= 4 {
			url = "http://" + os.Args[3] + ":1162"
		} else {
			url = "http://localhost:1162"
		}
		getlib.DownloadFile(url, source, dest, "")
	} else {
		fmt.Println("Need file/dir to download")
	}
}
