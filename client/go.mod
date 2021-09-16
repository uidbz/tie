module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20210916070403-d64f099eb7da
	git.sr.ht/~uid/tie/request v0.0.0-20210916070403-d64f099eb7da
	git.sr.ht/~uid/tie/tiedb v0.0.0-20210916070403-d64f099eb7da
	github.com/go-resty/resty/v2 v2.6.0
	golang.org/x/net v0.0.0-20210916014120-12bc252f5db8 // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
