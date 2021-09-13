module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.0.4
	git.sr.ht/~uid/tie/client v0.0.0-20210913162750-3bfb3f58467e
	git.sr.ht/~uid/tie/request v0.0.0-20210913162750-3bfb3f58467e
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
