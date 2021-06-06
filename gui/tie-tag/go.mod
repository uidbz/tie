module git.sr.ht/~uid/tie/gui/fileinfo

go 1.16

require (
	fyne.io/fyne/v2 v2.0.3
	git.sr.ht/~uid/putlib v0.0.0-20210504180720-4aba3cf34713
	git.sr.ht/~uid/tie/client v0.0.0-20210512063039-c632728f5a76
	git.sr.ht/~uid/tie/gui/component v0.0.0-20210511205824-eda5c64cdc10
	github.com/go-gl/gl v0.0.0-20210501111010-69f74958bac0 // indirect
	github.com/srwiley/oksvg v0.0.0-20210320200257-875f767ac39a // indirect
	github.com/stretchr/testify v1.7.0 // indirect
	golang.org/x/image v0.0.0-20210504121937-7319ad40d33e // indirect
	golang.org/x/net v0.0.0-20210510120150-4163338589ed // indirect
	golang.org/x/sys v0.0.0-20210511113859-b0526f3d8744 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client
