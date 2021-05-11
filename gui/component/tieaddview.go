package component

import (
	"encoding/json"
	"fmt"

	"fyne.io/fyne/v2"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"git.sr.ht/~uid/tie/client"
	"git.sr.ht/~uid/tie/request"
)

type TieAddView struct {
	Object              fyne.CanvasObject
	SelectedKey         binding.String
	SelectedValue1      binding.String
	SelectedValue2      binding.String
	SelectedTagCategory binding.String
	SelectedValue1ID    int
	SelectedValue2ID    int
	value1tovalue2      map[string][]string
	value1              []string
	uniquevalue1        []string
	value2              []string
	value2tovalue1      []string
	selectedTagsVal1    []string
	selectedTagsVal2    []string

	data1 binding.ExternalStringList
	data2 binding.ExternalStringList

	list1   *widget.List
	list2   *widget.List
	selTags *widget.List

	parent *TieView
}

func NewTieAddView() *TieAddView {
	tv := TieAddView{}
	tv.data1 = binding.BindStringList(&[]string{})
	tv.data2 = binding.BindStringList(&[]string{})
	tv.SelectedKey = binding.NewString()
	tv.SelectedValue1 = binding.NewString()
	tv.SelectedValue2 = binding.NewString()
	tv.SelectedTagCategory = binding.NewString()
	tv.SelectedTagCategory.Set("tag")
	tv.value1tovalue2 = make(map[string][]string)

	return &tv
}

func (tv *TieAddView) SetKey(key string) {
	tv.SelectedKey.Set(key)
}

func (tv *TieAddView) SetData(value1 []string, value2 []string) {
	data1 := make([]string, 1)
	tv.value2tovalue1 = make([]string, len(value2))
	data1[0] = "All values"

	if len(value1) != 0 {
		for i, x := range value1 {
			found := -1
			for j, y := range data1 {
				if x == y {
					found = j
					break
				}
			}
			currentValue1 := value1[i]
			currentValue2 := value2[i]
			if found != -1 {
				tv.value2tovalue1[i] = currentValue1
				tv.value1tovalue2[currentValue1] = append(tv.value1tovalue2[currentValue1], currentValue2)
			} else {
				data1 = append(data1, x)
				tv.value1tovalue2[currentValue1] = make([]string, 1)
				tv.value1tovalue2[currentValue1][0] = currentValue2
				tv.value2tovalue1[i] = currentValue1
			}
		}
	}

	tv.uniquevalue1 = data1
	tv.value1 = value1
	tv.value2 = value2
	tv.data1.Set(data1)
	tv.data2.Set(value2)
}

func (tv *TieAddView) GetTags() {
	g := request.Get{
		Values: []string{"tag"},
	}
	b, _ := json.Marshal(g)
	tie.SendToWebservice("Get", b, tv.GetHandler)
}

func (tv *TieAddView) GetHandler(resp json.RawMessage) {
	var result []request.ReplyGet
	err := json.Unmarshal(resp, &result)
	if err != nil {
		fmt.Println("Error handling Get reponse:", err)
	}

	fmt.Println(result)
	if len(result) > 0 {
		tv.SetData(result[0].Relations, result[0].Associations)
	}
}

func (tv *TieAddView) List1Select(id int) {
	selected := tv.uniquevalue1[id]
	tv.SelectedValue1ID = id

	if id == 0 { // 'All values' selected
		tv.data2.Set(tv.value2)
		tv.SelectedValue1.Set("")
		tv.list2.Unselect(tv.SelectedValue2ID)
		return
	}
	vals := make([]string, len(tv.value1tovalue2[selected]))
	i := 0
	for _, x := range tv.value1tovalue2[selected] {
		vals[i] = x
		i++
	}
	tv.SelectedValue1.Set(selected)
	tv.data2.Set(vals)
	tv.list2.Unselect(tv.SelectedValue2ID)
}

func (tv *TieAddView) List2Select(id int) {
	d, _ := tv.data2.Get()
	tv.SelectedValue2.Set(d[id])
	if tv.SelectedValue1ID == 0 {
		tv.SelectedValue1.Set(tv.value2tovalue1[id])
	}
	tv.SelectedValue2ID = id

	// Add to tag list
	val1, _ := tv.SelectedValue1.Get()
	val2, _ := tv.SelectedValue2.Get()
	tv.selectedTagsVal1 = append(tv.selectedTagsVal1, val1)
	tv.selectedTagsVal2 = append(tv.selectedTagsVal2, val2)
	tv.selTags.Refresh()
}

func RemoveAtIndex(s []string, index int) []string {
	ret := make([]string, 0)
	ret = append(ret, s[:index]...)
	return append(ret, s[index+1:]...)
}

func (tv *TieAddView) RemoveTag(id int) {
	tv.selectedTagsVal1 = RemoveAtIndex(tv.selectedTagsVal1, id)
	tv.selectedTagsVal1 = RemoveAtIndex(tv.selectedTagsVal1, id)
	tv.selTags.Refresh()
}

func (tv *TieAddView) Add() {
	key, _ := tv.SelectedKey.Get()
	// tv.selectedTags.Set(tv.selectedTagsList)
	// tv.Object.Refresh()
	// tv.window.SetContent(tv.MakeUI(tv.window))
	// a := request.Add{
	// 	key,
	// 	"hej",
	// 	"hej",
	// }
	// b, _ := json.Marshal(a)
	// tie.SendToWebservice("Add", b, AddHandler)
	for _, x := range tv.selectedTagsVal2 {
		tie.TieAdd(key, "tag", x, tv.parent.AddHandler)
	}
	tv.parent.window.SetContent(tv.parent.MakeUI(tv.parent.window))

}

func (tv *TieAddView) Exit() {
	tv.parent.window.SetContent(tv.parent.MakeUI(tv.parent.window))
}

func (tv *TieAddView) Del() {

}

func (tv *TieAddView) GetLengthTags() int {
	return len(tv.selectedTagsVal1)
}

func (tv *TieAddView) GetObj() fyne.CanvasObject {
	return widget.NewLabel("Template")
}

func (tv *TieAddView) UpdateTextTag(row int, obj fyne.CanvasObject) {
	obj.(*widget.Label).SetText(tv.selectedTagsVal1[row] + "/t" + tv.selectedTagsVal2[row])
}

func (tv *TieAddView) MakeUI(parent *TieView) fyne.CanvasObject {
	tv.parent = parent

	tv.list1 = widget.NewListWithData(tv.data1,
		func() fyne.CanvasObject {
			return widget.NewLabel("template")
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			o.(*widget.Label).Bind(i.(binding.String))
		})

	tv.list2 = widget.NewListWithData(tv.data2,
		func() fyne.CanvasObject {
			return widget.NewLabel("template")
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			o.(*widget.Label).Bind(i.(binding.String))
		})

	tv.list1.OnSelected = tv.List1Select
	tv.list2.OnSelected = tv.List2Select

	tv.list2.OnUnselected = func(id int) {
		tv.SelectedValue2.Set("")
	}

	split := container.NewHSplit(tv.list1, tv.list2)
	// lblSelected := widget.NewLabel("Selected tags:")
	// form := widget.NewForm(tv.selectedTags...)
	tv.selTags = widget.NewList(tv.GetLengthTags, tv.GetObj, tv.UpdateTextTag)
	tv.selTags.OnSelected = tv.RemoveTag
	middle := container.NewGridWithRows(2, split,
		container.NewBorder(widget.NewLabel("Selected tags"), nil, nil, nil, tv.selTags))

	// edit := widget.NewButton("Save", tv.Edit)
	exit := widget.NewButton("Close", tv.Exit)
	del := widget.NewButton("Del", tv.Del)
	add := widget.NewButton("Add tags", tv.Add)

	txtKey := widget.NewEntryWithData(tv.SelectedKey)
	txtKey.Disable()
	txtTagCategory := widget.NewEntryWithData(tv.SelectedTagCategory)
	top := container.NewGridWithRows(2, txtKey, txtTagCategory)
	txtValue1 := widget.NewEntryWithData(tv.SelectedValue1)
	txtValue1.Disable()
	txtValue2 := widget.NewEntryWithData(tv.SelectedValue2)
	buttons := container.NewGridWithColumns(3, exit, del, add)
	entryFields := container.NewGridWithColumns(2, txtValue1, txtValue2)
	bottom := container.NewGridWithRows(2, entryFields, buttons)

	return container.NewBorder(top, bottom, nil, nil, middle)
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
