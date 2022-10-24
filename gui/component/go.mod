module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.2.3
	git.sr.ht/~uid/tie/client v0.0.0-20220604100723-7141445d2e5e
	git.sr.ht/~uid/tie/request v0.0.0-20220604100723-7141445d2e5e
	github.com/fredbi/uri v0.0.0-20221012073901-fb871453c6d3 // indirect
	github.com/fyne-io/gl-js v0.0.0-20220802150000-8e339395f381 // indirect
	github.com/goki/freetype v0.0.0-20220119013949-7a161fd3728c // indirect
	github.com/srwiley/oksvg v0.0.0-20221011165216-be6e8873101c // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	github.com/yuin/goldmark v1.5.2 // indirect
	golang.org/x/image v0.1.0 // indirect
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
