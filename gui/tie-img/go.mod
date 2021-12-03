module tie-img

go 1.17

require (
	fyne.io/fyne/v2 v2.1.1
	git.sr.ht/~uid/tie/client v0.0.0-20211021072118-10d9aee6037e
	git.sr.ht/~uid/tie/gui/component v0.0.0-20211026072142-3f546ca4c972
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211021072118-10d9aee6037e
	github.com/disintegration/gift v1.2.1
	github.com/disintegration/imaging v1.6.2
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646
	github.com/rwcarlsen/goexif v0.0.0-20190401172101-9e8deecbddbd
)

require (
	git.sr.ht/~uid/tie/metadata v0.0.0-20211021072118-10d9aee6037e // indirect
	git.sr.ht/~uid/tie/request v0.0.0-20211021072118-10d9aee6037e // indirect
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211021072118-10d9aee6037e // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fredbi/uri v0.0.0-20181227131451-3dcfdacbaaf3 // indirect
	github.com/fsnotify/fsnotify v1.4.9 // indirect
	github.com/go-gl/gl v0.0.0-20210813123233-e4099ee2221f // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20210410170116-ea3d685f79fb // indirect
	github.com/go-resty/resty/v2 v2.6.0 // indirect
	github.com/godbus/dbus/v5 v5.0.4 // indirect
	github.com/goki/freetype v0.0.0-20181231101311-fa8a33aabaff // indirect
	github.com/minio/highwayhash v1.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/srwiley/oksvg v0.0.0-20210519022825-9fc0c575d5fe // indirect
	github.com/srwiley/rasterx v0.0.0-20210519020934-456a8d69b780 // indirect
	github.com/stretchr/testify v1.5.1 // indirect
	github.com/yuin/goldmark v1.4.2 // indirect
	golang.org/x/image v0.0.0-20210628002857-a66eb6448b8d // indirect
	golang.org/x/net v0.0.0-20211020060615-d418f374d309 // indirect
	golang.org/x/sys v0.0.0-20211025201205-69cdffdb9359 // indirect
	golang.org/x/text v0.3.7 // indirect
	gopkg.in/yaml.v2 v2.2.8 // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib

replace git.sr.ht/~uid/tie/request => ../../request

replace git.sr.ht/~uid/tie/metadata => ../../metadata

replace git.sr.ht/~uid/tie/tiedb => ../../tiedb
