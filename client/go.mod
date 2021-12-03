module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211026072142-3f546ca4c972
	git.sr.ht/~uid/tie/request v0.0.0-20211203083031-2aed8badebfd
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211026072142-3f546ca4c972
	github.com/go-resty/resty/v2 v2.7.0
	golang.org/x/net v0.0.0-20211201190559-0a0e4e1bb54c // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
