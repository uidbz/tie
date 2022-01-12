module git.sr.ht/~uid/tie/gui/tie-tag

go 1.16

require (
	fyne.io/fyne/v2 v2.1.2
	git.sr.ht/~uid/tie/client v0.0.0-20211206214737-0d583b9f07b4
	git.sr.ht/~uid/tie/gui/component v0.0.0-20220112073810-652cc9e95a17
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211206214737-0d583b9f07b4
	git.sr.ht/~uid/tie/request v0.0.0-20211206214737-0d583b9f07b4
	github.com/go-gl/gl v0.0.0-20211210172815-726fda9656d6 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20211213063430-748e38ca8aec // indirect
	github.com/godbus/dbus/v5 v5.0.6 // indirect
	github.com/spf13/cobra v1.3.0
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
