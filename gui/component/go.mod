module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.0.3
	git.sr.ht/~uid/tie/client v0.0.0-20210718194849-e88c154aed48
	git.sr.ht/~uid/tie/request v0.0.0-20210718194849-e88c154aed48
	golang.org/x/sys v0.0.0-20210630005230-0f9fa26af87c // indirect
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client
