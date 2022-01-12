module tie-img

go 1.17

require (
	fyne.io/fyne/v2 v2.1.2
	git.sr.ht/~uid/tie/client v0.0.0-20211206214737-0d583b9f07b4
	git.sr.ht/~uid/tie/gui/component v0.0.0-20220112073810-652cc9e95a17
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211206214737-0d583b9f07b4
	github.com/disintegration/gift v1.2.1
	github.com/disintegration/imaging v1.6.2
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646
	github.com/rwcarlsen/goexif v0.0.0-20190401172101-9e8deecbddbd
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20211206214737-0d583b9f07b4 // indirect
	git.sr.ht/~uid/tie/request v0.0.0-20211206214737-0d583b9f07b4 // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211206214737-0d583b9f07b4 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fredbi/uri v0.0.0-20181227131451-3dcfdacbaaf3 // indirect
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/go-gl/gl v0.0.0-20211210172815-726fda9656d6 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20211213063430-748e38ca8aec // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/godbus/dbus/v5 v5.0.6 // indirect
	github.com/goki/freetype v0.0.0-20181231101311-fa8a33aabaff // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/srwiley/oksvg v0.0.0-20211120171407-1837d6608d8c // indirect
	github.com/srwiley/rasterx v0.0.0-20210519020934-456a8d69b780 // indirect
	github.com/stretchr/testify v1.7.0 // indirect
	github.com/yuin/goldmark v1.4.4 // indirect
	golang.org/x/image v0.0.0-20211028202545-6944b10bf410 // indirect
	golang.org/x/net v0.0.0-20220111093109-d55c255bac03 // indirect
	golang.org/x/sys v0.0.0-20220111092808-5a964db01320 // indirect
	golang.org/x/text v0.3.7 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/metadata => ../../metadata

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
