module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211004073741-504d6e3f201d
	git.sr.ht/~uid/tie/request v0.0.0-20210927122737-608e131cb3d2
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211004073741-504d6e3f201d
	github.com/go-resty/resty/v2 v2.6.0
	golang.org/x/net v0.0.0-20210929193557-e81a3d93ecf6 // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
