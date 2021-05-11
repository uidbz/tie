module git.sr.ht/~uid/tie/gui/fileinfo

go 1.16

require (
	fyne.io/fyne/v2 v2.0.3
	git.sr.ht/~uid/putlib v0.0.0-20210504180720-4aba3cf34713
	git.sr.ht/~uid/tie v0.0.0-20210505074052-d36af4cfbb82
	git.sr.ht/~uid/tie/gui/component v0.0.0-20210504182642-0b159d4f3daf
	git.sr.ht/~uid/tie/request v0.0.0-20210511184902-7047c326fa57 // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210511182523-385b72bb0441 // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie => ../..
