module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.0.3
	git.sr.ht/~uid/tie/client v0.0.0-20210606173009-dd352469fcb8
	git.sr.ht/~uid/tie/request v0.0.0-20210609071741-dcd5d7fe16b1
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client
