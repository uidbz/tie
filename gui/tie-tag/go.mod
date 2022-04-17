module git.sr.ht/~uid/tie/gui/tie-tag

go 1.16

require (
	fyne.io/fyne/v2 v2.1.4
	git.sr.ht/~uid/tie/client v0.0.0-20220417162521-6ab633f69d4d
	git.sr.ht/~uid/tie/gui/component v0.0.0-20220417162521-6ab633f69d4d
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220417162521-6ab633f69d4d
	git.sr.ht/~uid/tie/request v0.0.0-20220417162521-6ab633f69d4d
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/go-gl/gl v0.0.0-20211210172815-726fda9656d6 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20220320163800-277f93cfa958 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/spf13/cobra v1.4.0
	github.com/stretchr/testify v1.7.1 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
