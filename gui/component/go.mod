module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.0.3
	git.sr.ht/~uid/tie/client v0.0.0-20210512063039-c632728f5a76 // indirect
	git.sr.ht/~uid/tie/request v0.0.0-20210511205824-eda5c64cdc10 // indirect
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client
