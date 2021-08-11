module git.sr.ht/~uid/tie/gui/tie-tag

go 1.16

require (
	fyne.io/fyne/v2 v2.0.4
	git.sr.ht/~uid/putlib v0.0.0-20210504180720-4aba3cf34713
	git.sr.ht/~uid/tie/client v0.0.0-20210803200836-8b2027240a81
	git.sr.ht/~uid/tie/gui/component v0.0.0-20210803200836-8b2027240a81
	github.com/go-gl/gl v0.0.0-20210501111010-69f74958bac0 // indirect
	github.com/go-gl/glfw/v3.3/glfw v0.0.0-20210727001814-0db043d8d5be // indirect
	github.com/srwiley/oksvg v0.0.0-20210519022825-9fc0c575d5fe // indirect
	github.com/srwiley/rasterx v0.0.0-20210519020934-456a8d69b780 // indirect
	github.com/stretchr/testify v1.7.0 // indirect
	golang.org/x/image v0.0.0-20210628002857-a66eb6448b8d // indirect
	golang.org/x/text v0.3.7 // indirect
	gopkg.in/yaml.v3 v3.0.0-20210107192922-496545a6307b // indirect
)

replace git.sr.ht/~uid/tie/gui/component => ../component

replace git.sr.ht/~uid/tie/client => ../../client

replace git.sr.ht/~uid/tie/io/putlib => ../../io/putlib
