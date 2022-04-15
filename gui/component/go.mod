module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.1.4
	git.sr.ht/~uid/tie/client v0.0.0-20220415093055-65579d4670d9
	git.sr.ht/~uid/tie/request v0.0.0-20220415093055-65579d4670d9
	github.com/goki/freetype v0.0.0-20220119013949-7a161fd3728c // indirect
	github.com/srwiley/oksvg v0.0.0-20220128195007-1f435e4c2b44 // indirect
	github.com/srwiley/rasterx v0.0.0-20220128185129-2efea2b9ea41 // indirect
	github.com/yuin/goldmark v1.4.11 // indirect
	golang.org/x/image v0.0.0-20220413100746-70e8d0d3baa9 // indirect
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
