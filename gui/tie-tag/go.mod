module git.sr.ht/~uid/tie/gui/tie-tag

go 1.16

require (
	fyne.io/fyne/v2 v2.3.0
	git.sr.ht/~uid/tie/client v0.0.0-20221024072834-4e948d6ac84e
	git.sr.ht/~uid/tie/gui/component v0.0.0-20230106084800-144edbfaae3b
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20221024072834-4e948d6ac84e
	git.sr.ht/~uid/tie/request v0.0.0-20221024072834-4e948d6ac84e
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/fyne-io/glfw-js v0.0.0-20220517201726-bebc2019cd33 // indirect
	github.com/fyne-io/image v0.0.0-20221020213044-f609c6a24345 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/cobra v1.6.1
	github.com/stretchr/testify v1.8.1 // indirect
	golang.org/x/mobile v0.0.0-20221110043201-43a038452099 // indirect
	honnef.co/go/js/dom v0.0.0-20221001195520-26252dedbe70 // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
