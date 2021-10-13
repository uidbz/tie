module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211011072406-3d9072e169be
	git.sr.ht/~uid/tie/request v0.0.0-20211011072406-3d9072e169be
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211011072406-3d9072e169be
	github.com/go-resty/resty/v2 v2.6.0
	golang.org/x/net v0.0.0-20211011170408-caeb26a5c8c0 // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
