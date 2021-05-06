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

type TieView struct {
	Object           fyne.CanvasObject
	SelectedKey      binding.String
	SelectedValue1   binding.String
	SelectedValue2   binding.String
	SelectedValue2ID int
	value1tovalue2   map[string][]string
	value1           []string
	uniquevalue1     []string
	value2           []string
	value2tovalue1   []string

	data1 binding.ExternalStringList
	data2 binding.ExternalStringList

	list1 *widget.List
	list2 *widget.List
}

func NewTieGUIComponent() *TieView {
	tv := TieView{}
	tv.data1 = binding.BindStringList(&[]string{})
	tv.data2 = binding.BindStringList(&[]string{})
	tv.SelectedKey = binding.NewString()
	tv.SelectedValue1 = binding.NewString()
	tv.SelectedValue2 = binding.NewString()
	tv.value1tovalue2 = make(map[string][]string)

	return &tv
}

type ValueCouple struct {
	Value1 string
	Value2 string
}

func (tv *TieView) SetKey(key string) {
	tv.SelectedKey.Set(key)
}

func (tv *TieView) SetData(value1 []string, value2 []string) {
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

func (tv *TieView) List1Select(id int) {
	selected := tv.uniquevalue1[id]

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

func (tv *TieView) List2Select(id int) {
	d, _ := tv.data2.Get()
	tv.SelectedValue2.Set(d[id])
	tv.SelectedValue1.Set(tv.value2tovalue1[id])
	tv.SelectedValue2ID = id

}

func (tv *TieView) Add() {
	key, _ := tv.SelectedKey.Get()
	a := request.Add{
		key,
		"hej",
		"hej",
	}
	b, _ := json.Marshal(a)
	tie.SendToWebservice("Add", b, AddHandler)

}

func (tv *TieView) Edit() {

}

func (tv *TieView) Del() {

}

func (tv *TieView) MakeUI() fyne.CanvasObject {
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

	add := widget.NewButton("Add", tv.Add)
	edit := widget.NewButton("Edit", tv.Edit)
	del := widget.NewButton("Del", tv.Del)

	txtKey := widget.NewEntryWithData(tv.SelectedKey)
	txtKey.Disable()
	txtValue1 := widget.NewEntryWithData(tv.SelectedValue1)
	txtValue1.Disable()
	txtValue2 := widget.NewEntryWithData(tv.SelectedValue2)
	buttons := container.NewGridWithColumns(3, add, edit, del)
	entryFields := container.NewGridWithColumns(2, txtValue1, txtValue2)
	bottom := container.NewGridWithRows(2, entryFields, buttons)

	return container.NewBorder(txtKey, bottom, nil, nil, split)
}

func AddHandler(resp json.RawMessage) {
	s := request.ReplyStatus{}
	if err := json.Unmarshal(resp, &s); err == nil {
		if tie.CurrentState.Verbose || !s.Success {
			fmt.Println(resp)
		}
	} else {
		fmt.Println("Error unmarshalling response:", err, resp)
	}

}
