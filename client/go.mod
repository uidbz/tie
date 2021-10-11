module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20211004073857-fca4a4dd4412
	git.sr.ht/~uid/tie/request v0.0.0-20211004073857-fca4a4dd4412
	git.sr.ht/~uid/tie/tiedb v0.0.0-20211011072131-5231bfa7eb98
	github.com/go-resty/resty/v2 v2.6.0
	golang.org/x/net v0.0.0-20211008194852-3b03d305991f // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
