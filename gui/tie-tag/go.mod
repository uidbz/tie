module git.sr.ht/~uid/tie/gui/tie-tag

go 1.16

require (
	fyne.io/fyne/v2 v2.1.1
	git.sr.ht/~uid/tie/client v0.0.0-20211203084034-9d28a8705a27
	git.sr.ht/~uid/tie/gui/component v0.0.0-20211203084034-9d28a8705a27
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211203084034-9d28a8705a27
	git.sr.ht/~uid/tie/request v0.0.0-20211203084034-9d28a8705a27
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/go-gl/gl v0.0.0-20211025173605-bda47ffaa784 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20211024062804-40e447a793be // indirect
	github.com/godbus/dbus/v5 v5.0.6 // indirect
	github.com/spf13/cobra v1.2.1
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
