module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.0.4
	git.sr.ht/~uid/tie/client v0.0.0-20210913163046-87b8501c7403
	git.sr.ht/~uid/tie/request v0.0.0-20210913163046-87b8501c7403
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
