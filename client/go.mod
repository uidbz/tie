module git.sr.ht/~uid/tie/client

go 1.16

require (
	git.sr.ht/~uid/tie/io/putlib v0.0.0-20220112201315-398bccc5acac
	git.sr.ht/~uid/tie/request v0.0.0-20220205171529-6a3d524edd61
	git.sr.ht/~uid/tie/tiedb v0.0.0-20220205171529-6a3d524edd61
	github.com/go-resty/resty/v2 v2.7.0
	golang.org/x/net v0.0.0-20220127200216-cd36cc0744dd // indirect
)

replace git.sr.ht/~uid/tie/request => ../request

replace git.sr.ht/~uid/tie/tiedb => ../tiedb

replace git.sr.ht/~uid/tie/io/putlib => ../io/putlib
