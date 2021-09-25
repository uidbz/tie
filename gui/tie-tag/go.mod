module git.sr.ht/~uid/tie/gui/tie-tag

go 1.16

require (
	fyne.io/fyne/v2 v2.1.0
	git.sr.ht/~uid/tie/client v0.0.0-20210924183216-63054f9bcd07
	git.sr.ht/~uid/tie/gui/component v0.0.0-20210924183216-63054f9bcd07
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210924183216-63054f9bcd07
	git.sr.ht/~uid/tie/request v0.0.0-20210924183216-63054f9bcd07
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/go-gl/gl v0.0.0-20210905235341-f7a045908259 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20210727001814-0db043d8d5be // indirect
	github.com/godbus/dbus/v5 v5.0.5 // indirect
	github.com/stretchr/testify v1.7.0 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
