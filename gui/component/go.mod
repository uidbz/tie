module git.sr.ht/~uid/tie/gui/component

go 1.16

require (
	fyne.io/fyne/v2 v2.1.2
	git.sr.ht/~uid/tie/client v0.0.0-20211206214737-0d583b9f07b4
	git.sr.ht/~uid/tie/request v0.0.0-20211206214737-0d583b9f07b4
	github.com/srwiley/oksvg v0.0.0-20211120171407-1837d6608d8c // indirect
	github.com/srwiley/rasterx v0.0.0-20210519020934-456a8d69b780 // indirect
	github.com/yuin/goldmark v1.4.4 // indirect
	golang.org/x/image v0.0.0-20211028202545-6944b10bf410 // indirect
	golang.org/x/text v0.3.7 // indirect
)

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
