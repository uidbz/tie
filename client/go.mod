module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220417162659-7bbeb6d035c3
	git.sr.ht/~uid/tie/request v0.0.0-20220417162659-7bbeb6d035c3
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220417162659-7bbeb6d035c3
	github.com/go-resty/resty/v2 v2.7.0
	golang.org/x/net v0.0.0-20220420153159-1850ba15e1be // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
