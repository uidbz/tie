module git.sr.ht/~uid/tie/gui/tie-tag

go 1.16

require (
	fyne.io/fyne/v2 v2.2.3
	git.sr.ht/~uid/tie/client v0.0.0-20220604100723-7141445d2e5e
	git.sr.ht/~uid/tie/gui/component v0.0.0-20221024072707-e9d3c2d8e9bf
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20221024072707-e9d3c2d8e9bf
	git.sr.ht/~uid/tie/request v0.0.0-20220604100723-7141445d2e5e
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/fyne-io/glfw-js v0.0.0-20220517201726-bebc2019cd33 // indirect
	github.com/fyne-io/image v0.0.0-20221020213044-f609c6a24345 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20221017161538-93cebf72946b // indirect
	github.com/spf13/cobra v1.6.0
	github.com/stretchr/testify v1.8.1 // indirect
	golang.org/x/mobile v0.0.0-20221020085226-b36e6246172e // indirect
	honnef.co/go/js/dom v0.0.0-20221001195520-26252dedbe70 // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
