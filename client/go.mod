module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220410090907-a8464ea42d4b
	git.sr.ht/~uid/tie/request v0.0.0-20220410090907-a8464ea42d4b
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220410090907-a8464ea42d4b
	github.com/go-resty/resty/v2 v2.7.0
	golang.org/x/net v0.0.0-20220407224826-aac1ed45d8e3 // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
