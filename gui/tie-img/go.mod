module tie-img

go 1.17

require (
	fyne.io/fyne/v2 v2.1.4
	git.sr.ht/~uid/tie/client v0.0.0-20220417162521-6ab633f69d4d
	git.sr.ht/~uid/tie/gui/component v0.0.0-20220417162521-6ab633f69d4d
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220417162521-6ab633f69d4d
	github.com/disintegration/gift v1.2.1
	github.com/disintegration/imaging v1.6.2
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646
	github.com/rwcarlsen/goexif v0.0.0-20190401172101-9e8deecbddbd
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20220417162521-6ab633f69d4d // indirect
	git.sr.ht/~uid/tie/request v0.0.0-20220417162521-6ab633f69d4d // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220417162521-6ab633f69d4d // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fredbi/uri v0.0.0-20181227131451-3dcfdacbaaf3 // indirect
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/go-gl/gl v0.0.0-20211210172815-726fda9656d6 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20220320163800-277f93cfa958 // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/goki/freetype v0.0.0-20220119013949-7a161fd3728c // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/srwiley/oksvg v0.0.0-20220128195007-1f435e4c2b44 // indirect
	github.com/srwiley/rasterx v0.0.0-20220128185129-2efea2b9ea41 // indirect
	github.com/stretchr/testify v1.7.1 // indirect
	github.com/yuin/goldmark v1.4.11 // indirect
	golang.org/x/image v0.0.0-20220413100746-70e8d0d3baa9 // indirect
	golang.org/x/net v0.0.0-20220412020605-290c469a71a5 // indirect
	golang.org/x/sys v0.0.0-20220412211240-33da011f77ad // indirect
	golang.org/x/text v0.3.7 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/metadata => ../../metadata

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
