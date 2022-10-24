module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20221024072707-e9d3c2d8e9bf
	git.sr.ht/~uid/tie/request v0.0.0-20220604100723-7141445d2e5e
	git.sr.ht/~uid/tie/tiedb v0.0.0-20221024072707-e9d3c2d8e9bf
	github.com/go-resty/resty/v2 v2.7.0
	golang.org/x/net v0.1.0 // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
