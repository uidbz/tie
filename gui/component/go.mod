module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.1.0
	git.sr.ht/~uid/tie/client v0.0.0-20211013062036-447ddea6c8e2
	git.sr.ht/~uid/tie/request v0.0.0-20211013062036-447ddea6c8e2
	github.com/srwiley/oksvg v0.0.0-20210519022825-9fc0c575d5fe // indirect
	github.com/srwiley/rasterx v0.0.0-20210519020934-456a8d69b780 // indirect
	github.com/yuin/goldmark v1.4.1 // indirect
	golang.org/x/image v0.0.0-20210628002857-a66eb6448b8d // indirect
	golang.org/x/text v0.3.7 // indirect
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
