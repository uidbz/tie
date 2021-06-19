module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.0.3
	git.sr.ht/~uid/tie/client v0.0.0-20210619184020-b0affc015bfc
	git.sr.ht/~uid/tie/request v0.0.0-20210619183836-21eb3cdfb30a
	golang.org/x/sys v0.0.0-20210616094352-59db8d763f22 // indirect
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client
